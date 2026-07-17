package messaging

import (
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const (
	feishuWorkspaceChoiceTTL         = 5 * time.Minute
	feishuWorkspaceChoiceTokenPrefix = "@ws_"
	feishuWorkspaceChoiceCodex       = "codex"
	feishuWorkspaceChoiceClaude      = "claude"
)

type feishuWorkspaceChoiceRecord struct {
	kind          string
	actorUserID   string
	bindingKey    string
	workspaceRoot string
	expiresAt     time.Time
}

// feishuWorkspaceChoiceStore 只在服务端保存工作空间路径，卡片仅携带短期 opaque token。
type feishuWorkspaceChoiceStore struct {
	mu      sync.Mutex
	records map[string]feishuWorkspaceChoiceRecord
	now     func() time.Time
}

func (s *feishuWorkspaceChoiceStore) issue(kind string, actorUserID string, bindingKey string, workspaceRoot string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowOrDefault()
	s.purgeExpiredLocked(now)
	if s.records == nil {
		s.records = make(map[string]feishuWorkspaceChoiceRecord)
	}
	token := feishuWorkspaceChoiceTokenPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	s.records[token] = feishuWorkspaceChoiceRecord{
		kind: strings.TrimSpace(kind), actorUserID: strings.TrimSpace(actorUserID),
		bindingKey: strings.TrimSpace(bindingKey), workspaceRoot: strings.TrimSpace(workspaceRoot),
		expiresAt: now.Add(feishuWorkspaceChoiceTTL),
	}
	return token
}

// consume 原子校验窗口和操作者并一次性消费 token；错误窗口不得耗掉原持有人的 token。
func (s *feishuWorkspaceChoiceStore) consume(token string, kind string, actorUserID string, bindingKey string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowOrDefault()
	s.purgeExpiredLocked(now)
	record, ok := s.records[strings.TrimSpace(token)]
	if !ok || record.kind != strings.TrimSpace(kind) ||
		record.actorUserID != strings.TrimSpace(actorUserID) || record.bindingKey != strings.TrimSpace(bindingKey) {
		return "", false
	}
	delete(s.records, strings.TrimSpace(token))
	return record.workspaceRoot, true
}

func (s *feishuWorkspaceChoiceStore) purgeExpiredLocked(now time.Time) {
	for token, record := range s.records {
		if !record.expiresAt.After(now) {
			delete(s.records, token)
		}
	}
}

func (s *feishuWorkspaceChoiceStore) nowOrDefault() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func isFeishuWorkspaceChoiceToken(target string) bool {
	target = strings.TrimSpace(target)
	if len(target) != len(feishuWorkspaceChoiceTokenPrefix)+32 || !strings.HasPrefix(target, feishuWorkspaceChoiceTokenPrefix) {
		return false
	}
	for _, char := range target[len(feishuWorkspaceChoiceTokenPrefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// isLegacyFeishuWorkspaceChoice 拒绝升级前按列表下标编码的旧卡片，避免目录变化后误选。
func isLegacyFeishuWorkspaceChoice(msg platform.IncomingMessage, command string) bool {
	if msg.Platform != platform.PlatformFeishu || msg.RawCommand == nil || msg.RawCommand.Action != "choice" {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) != 3 || fields[1] != "cd" || (fields[0] != "/cx" && fields[0] != "/cc") {
		return false
	}
	_, numeric := parseCodexListIndex(fields[2])
	return numeric
}
