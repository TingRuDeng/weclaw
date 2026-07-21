package feishu

import (
	"log"
	"strconv"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const feishuMirrorDedupWindow = time.Second

type pendingGroupMirror struct {
	base     string
	eventAt  time.Time
	timer    *time.Timer
	canceled bool
	dispatch func()
	consume  func() error
	cleanup  func()
}

type feishuMirrorFingerprint struct {
	base    string
	eventAt time.Time
}

type feishuMirrorStamp struct {
	eventAt time.Time
	seenAt  time.Time
}

// recordThreadMirrorFingerprint 记录 thread 消息，并取消同一意图的群聊镜像 pending。
func (d *feishuEventDeduper) recordThreadMirrorFingerprint(event *larkim.P2MessageReceiveV1, scope FeishuSessionScope, content string) error {
	if d == nil || !hasThreadFields(scope) {
		return nil
	}
	fp, ok := feishuMirrorFingerprintFor(event, scope, content)
	if !ok {
		return nil
	}
	now := d.now()
	var canceled []*pendingGroupMirror
	d.mu.Lock()
	d.cleanupMirrorLocked(now)
	d.mirrorThreadSeen[fp.base] = append(d.mirrorThreadSeen[fp.base], feishuMirrorStamp{eventAt: fp.eventAt, seenAt: now})
	canceled = d.cancelMatchingPendingMirrorsLocked(fp)
	d.mu.Unlock()
	for _, pending := range canceled {
		if pending.timer != nil {
			pending.timer.Stop()
		}
		if err := pending.consume(); err != nil {
			pending.cleanup()
			log.Printf("[feishu] failed to consume canceled group mirror: %v", err)
			continue
		}
		pending.cleanup()
		log.Printf("[feishu] dropped group mirror after thread event")
	}
	return nil
}

// deferPossibleGroupMirror 将疑似群聊镜像延迟一个短窗口；若窗口内未出现 thread，则正常分发。
func (d *feishuEventDeduper) deferPossibleGroupMirror(event *larkim.P2MessageReceiveV1, scope FeishuSessionScope, content string, dispatch func(), consume func() error, cleanup func()) (bool, error) {
	if d == nil || !isPossibleGroupMirror(scope, content) {
		return false, nil
	}
	fp, ok := feishuMirrorFingerprintFor(event, scope, content)
	if !ok {
		return false, nil
	}
	now := d.now()
	d.mu.Lock()
	d.cleanupMirrorLocked(now)
	if d.hasMatchingThreadLocked(fp) {
		d.mu.Unlock()
		if err := consume(); err != nil {
			cleanup()
			return true, err
		}
		cleanup()
		log.Printf("[feishu] dropped group mirror after thread event")
		return true, nil
	}
	pending := &pendingGroupMirror{base: fp.base, eventAt: fp.eventAt, dispatch: dispatch, consume: consume, cleanup: cleanup}
	pending.timer = time.AfterFunc(d.mirrorWindow, func() {
		if d.releasePendingMirror(pending) {
			log.Printf("[feishu] dispatching pending group message")
			pending.dispatch()
		}
	})
	d.pendingMirrors[fp.base] = append(d.pendingMirrors[fp.base], pending)
	d.mu.Unlock()
	log.Printf("[feishu] pending possible group mirror")
	return true, nil
}

// releasePendingMirror 在等待窗口结束后释放未被 thread 取消的群消息。
func (d *feishuEventDeduper) releasePendingMirror(pending *pendingGroupMirror) bool {
	if d == nil || pending == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	items := d.pendingMirrors[pending.base]
	for i, item := range items {
		if item != pending {
			continue
		}
		d.pendingMirrors[pending.base] = append(items[:i], items[i+1:]...)
		if len(d.pendingMirrors[pending.base]) == 0 {
			delete(d.pendingMirrors, pending.base)
		}
		return !pending.canceled
	}
	return false
}

// cancelMatchingPendingMirrorsLocked 取消同一用户、同一群、同一内容、短时间内的群镜像。
func (d *feishuEventDeduper) cancelMatchingPendingMirrorsLocked(fp feishuMirrorFingerprint) []*pendingGroupMirror {
	items := d.pendingMirrors[fp.base]
	if len(items) == 0 {
		return nil
	}
	kept := items[:0]
	canceled := make([]*pendingGroupMirror, 0, len(items))
	for _, pending := range items {
		if withinFeishuMirrorWindow(fp.eventAt, pending.eventAt, d.mirrorWindow) {
			pending.canceled = true
			canceled = append(canceled, pending)
			continue
		}
		kept = append(kept, pending)
	}
	if len(kept) == 0 {
		delete(d.pendingMirrors, fp.base)
	} else {
		d.pendingMirrors[fp.base] = kept
	}
	return canceled
}

// hasMatchingThreadLocked 判断是否已经见过同一意图的 thread 消息。
func (d *feishuEventDeduper) hasMatchingThreadLocked(fp feishuMirrorFingerprint) bool {
	for _, stamp := range d.mirrorThreadSeen[fp.base] {
		if withinFeishuMirrorWindow(fp.eventAt, stamp.eventAt, d.mirrorWindow) {
			return true
		}
	}
	return false
}

// cleanupMirrorLocked 清理短窗口外的 thread 指纹。
func (d *feishuEventDeduper) cleanupMirrorLocked(now time.Time) {
	cutoff := now.Add(-d.ttl)
	for base, times := range d.mirrorThreadSeen {
		kept := times[:0]
		for _, stamp := range times {
			if !stamp.seenAt.Before(cutoff) {
				kept = append(kept, stamp)
			}
		}
		if len(kept) == 0 {
			delete(d.mirrorThreadSeen, base)
		} else {
			d.mirrorThreadSeen[base] = kept
		}
	}
}

// feishuMirrorFingerprintFor 生成不含 message_id/thread_key 的镜像指纹。
func feishuMirrorFingerprintFor(event *larkim.P2MessageReceiveV1, scope FeishuSessionScope, content string) (feishuMirrorFingerprint, bool) {
	eventAt, ok := feishuMessageCreateTimeValue(event)
	if !ok {
		return feishuMirrorFingerprint{}, false
	}
	parts := []string{
		strings.TrimSpace(scope.TenantID),
		strings.TrimSpace(scope.ChatID),
		strings.TrimSpace(scope.SenderOpenID),
		hashString(strings.TrimSpace(content)),
	}
	for _, part := range parts {
		if part == "" {
			return feishuMirrorFingerprint{}, false
		}
	}
	return feishuMirrorFingerprint{base: strings.Join(parts, "\x00"), eventAt: eventAt}, true
}

// feishuMessageCreateTimeValue 将飞书毫秒时间戳转为时间值。
func feishuMessageCreateTimeValue(event *larkim.P2MessageReceiveV1) (time.Time, bool) {
	value := feishuMessageCreateTime(event)
	if value == "" {
		return time.Time{}, false
	}
	millis, err := strconv.ParseInt(value, 10, 64)
	if err != nil || millis <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(millis), true
}

// isPossibleGroupMirror 只延迟满足镜像特征的无 thread 群消息。
func isPossibleGroupMirror(scope FeishuSessionScope, content string) bool {
	return isFeishuGroupChat(scope.ChatType) &&
		!hasThreadFields(scope) &&
		strings.TrimSpace(scope.SenderOpenID) != "" &&
		strings.TrimSpace(scope.ChatID) != "" &&
		strings.TrimSpace(content) != ""
}

// withinFeishuMirrorWindow 判断两个事件是否处于同一个短镜像窗口。
func withinFeishuMirrorWindow(left time.Time, right time.Time, window time.Duration) bool {
	if window <= 0 {
		window = feishuMirrorDedupWindow
	}
	delta := left.Sub(right)
	if delta < 0 {
		delta = -delta
	}
	return delta <= window
}
