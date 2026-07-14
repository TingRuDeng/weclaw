package feishu

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const feishuEventDedupTTL = 10 * time.Minute

type feishuEventDeduper struct {
	mu               sync.Mutex
	seen             map[string]time.Time
	processing       map[string]feishuDedupClaim
	mirrorThreadSeen map[string][]feishuMirrorStamp
	pendingMirrors   map[string][]*pendingGroupMirror
	ttl              time.Duration
	stateFile        string
	mirrorWindow     time.Duration
	now              func() time.Time
	debug            bool
}

type feishuEventDedupState struct {
	Version int               `json:"version"`
	Seen    map[string]string `json:"seen"`
	Updated string            `json:"updated"`
}

type feishuDedupReservation struct {
	deduper *feishuEventDeduper
	keys    []string
	owner   *feishuDedupOwner
}

type feishuDedupClaim struct {
	owner *feishuDedupOwner
	at    time.Time
}

type feishuDedupOwner struct{ marker byte }

// newFeishuEventDeduper 创建飞书事件短期去重器。
func newFeishuEventDeduper(ttl time.Duration) *feishuEventDeduper {
	if ttl <= 0 {
		ttl = feishuEventDedupTTL
	}
	return &feishuEventDeduper{
		seen:             make(map[string]time.Time),
		processing:       make(map[string]feishuDedupClaim),
		mirrorThreadSeen: make(map[string][]feishuMirrorStamp),
		pendingMirrors:   make(map[string][]*pendingGroupMirror),
		ttl:              ttl,
		mirrorWindow:     feishuMirrorDedupWindow,
		now:              time.Now,
	}
}

// setStateFile 启用跨重启短期去重，避免飞书重投递旧消息时重复触发命令。
func (d *feishuEventDeduper) setStateFile(path string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.stateFile = strings.TrimSpace(path)
	d.loadStateLocked(d.now())
	d.mu.Unlock()
}

// isDuplicate 记录飞书事件短期指纹，避免长连接重投递导致同一输入重复进 agent。
func (d *feishuEventDeduper) isDuplicate(event *larkim.P2MessageReceiveV1, scope FeishuSessionScope) bool {
	reservation, duplicate := d.reserve(event, scope)
	if !duplicate {
		reservation.complete()
	}
	return duplicate
}

// reserve 原子登记事件指纹，并返回仅能释放本次写入的预约值。
func (d *feishuEventDeduper) reserve(event *larkim.P2MessageReceiveV1, scope FeishuSessionScope) (feishuDedupReservation, bool) {
	if d == nil {
		return feishuDedupReservation{}, false
	}
	keys := feishuDedupKeys(event, scope)
	if len(keys) == 0 {
		return feishuDedupReservation{}, false
	}
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleanupLocked(now)
	for _, key := range keys {
		if seenAt, ok := d.seen[key]; ok && now.Sub(seenAt) <= d.ttl {
			if d.debug {
				log.Printf("[feishu] ignored duplicate message event")
			}
			return feishuDedupReservation{}, true
		}
		if claim, ok := d.processing[key]; ok && now.Sub(claim.at) <= d.ttl {
			return feishuDedupReservation{}, true
		}
	}
	owner := &feishuDedupOwner{marker: 1}
	for _, key := range keys {
		d.processing[key] = feishuDedupClaim{owner: owner, at: now}
	}
	return feishuDedupReservation{deduper: d, keys: keys, owner: owner}, false
}

// complete 把仍归当前处理者所有的预约提交为完成记录，并返回所有权是否有效。
func (r feishuDedupReservation) complete() bool {
	if r.deduper == nil || r.owner == nil {
		return true
	}
	now := r.deduper.now()
	r.deduper.mu.Lock()
	owned := true
	for _, key := range r.keys {
		claim, ok := r.deduper.processing[key]
		if !ok || claim.owner != r.owner {
			owned = false
			break
		}
	}
	if !owned {
		r.deduper.mu.Unlock()
		return false
	}
	for _, key := range r.keys {
		delete(r.deduper.processing, key)
		r.deduper.seen[key] = now
	}
	r.deduper.persistStateLocked(now)
	r.deduper.mu.Unlock()
	return true
}

// release 仅回滚仍由当前预约持有的处理中指纹，避免删除并发处理者的新记录。
func (r feishuDedupReservation) release() {
	if r.deduper == nil || r.owner == nil {
		return
	}
	r.deduper.mu.Lock()
	for _, key := range r.keys {
		if claim, ok := r.deduper.processing[key]; ok && claim.owner == r.owner {
			delete(r.deduper.processing, key)
		}
	}
	r.deduper.mu.Unlock()
}

// cleanupLocked 清理超过 TTL 的历史指纹，调用方必须持有锁。
func (d *feishuEventDeduper) cleanupLocked(now time.Time) {
	cutoff := now.Add(-d.ttl)
	for key, seenAt := range d.seen {
		if seenAt.Before(cutoff) {
			delete(d.seen, key)
		}
	}
	for key, claim := range d.processing {
		if claim.at.Before(cutoff) {
			delete(d.processing, key)
		}
	}
}

func (d *feishuEventDeduper) loadStateLocked(now time.Time) {
	if d.stateFile == "" {
		return
	}
	data, err := os.ReadFile(d.stateFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[feishu] failed to read event dedup state %s: %v", d.stateFile, err)
		}
		return
	}
	var state feishuEventDedupState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("[feishu] failed to parse event dedup state %s: %v", d.stateFile, err)
		return
	}
	for key, rawTime := range state.Seen {
		seenAt, err := time.Parse(time.RFC3339Nano, rawTime)
		if err == nil && now.Sub(seenAt) <= d.ttl {
			d.seen[key] = seenAt
		}
	}
	d.cleanupLocked(now)
}

func (d *feishuEventDeduper) persistStateLocked(now time.Time) {
	if d.stateFile == "" {
		return
	}
	state := feishuEventDedupState{
		Version: 1,
		Seen:    make(map[string]string, len(d.seen)),
		Updated: now.UTC().Format(time.RFC3339Nano),
	}
	for key, seenAt := range d.seen {
		state.Seen[key] = seenAt.UTC().Format(time.RFC3339Nano)
	}
	writeFeishuEventDedupState(d.stateFile, state)
}

func writeFeishuEventDedupState(path string, state feishuEventDedupState) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("[feishu] failed to create event dedup state dir %s: %v", filepath.Dir(path), err)
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("[feishu] failed to marshal event dedup state: %v", err)
		return
	}
	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		log.Printf("[feishu] failed to write event dedup state %s: %v", tmpFile, err)
		return
	}
	if err := os.Rename(tmpFile, path); err != nil {
		log.Printf("[feishu] failed to move event dedup state into place %s: %v", path, err)
		_ = os.Remove(tmpFile)
	}
}

// feishuDedupKeys 生成多层去重 key：事件、消息、群聊 thread 内容指纹。
func feishuDedupKeys(event *larkim.P2MessageReceiveV1, scope FeishuSessionScope) []string {
	keys := make([]string, 0, 3)
	if eventID := feishuEventID(event); eventID != "" {
		keys = append(keys, "event:"+eventID)
	}
	if messageID := strings.TrimSpace(scope.MessageID); messageID != "" {
		keys = append(keys, "message:"+messageID)
	}
	if fallback := feishuContentDedupKey(event, scope); fallback != "" {
		keys = append(keys, "content:"+fallback)
	}
	return keys
}

// feishuEventID 读取飞书事件头 ID。
func feishuEventID(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil {
		return ""
	}
	return strings.TrimSpace(event.EventV2Base.Header.EventID)
}

// feishuContentDedupKey 生成群聊 thread 级内容指纹，避免不同 message_id 的同输入重复处理。
func feishuContentDedupKey(event *larkim.P2MessageReceiveV1, scope FeishuSessionScope) string {
	if !isFeishuGroupChat(scope.ChatType) {
		return ""
	}
	content := rawMessageContent(event)
	if strings.TrimSpace(content) == "" {
		return ""
	}
	parts := []string{
		strings.TrimSpace(scope.TenantID),
		strings.TrimSpace(scope.ChatID),
		ResolveThreadKey(scope),
		strings.TrimSpace(scope.SenderOpenID),
		feishuMessageCreateTime(event),
		hashString(content),
	}
	for _, part := range parts {
		if part == "" {
			return ""
		}
	}
	return strings.Join(parts, "\x00")
}

// feishuMessageCreateTime 读取飞书消息创建时间，作为内容去重的时间边界。
func feishuMessageCreateTime(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return ""
	}
	return stringValue(event.Event.Message.CreateTime)
}

// hashString 返回稳定短文本哈希，避免把完整消息内容放进去重 map。
func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
