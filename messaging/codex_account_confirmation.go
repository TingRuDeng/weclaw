package messaging

import (
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/google/uuid"
)

const (
	feishuCodexAccountConfirmTTL         = 5 * time.Minute
	feishuCodexAccountConfirmTokenPrefix = "@acct_"
)

type feishuCodexAccountConfirmScope struct {
	AccountID   string
	ActorUserID string
	RouteUserID string
}

type feishuCodexAccountConfirmation struct {
	scope         feishuCodexAccountConfirmScope
	profileID     agent.CodexAccountProfileID
	revision      uint64
	previousLabel string
	targetLabel   string
	expiresAt     time.Time
	consumed      bool
	completed     bool
	result        string
}

type feishuCodexAccountConfirmState int

const (
	feishuCodexAccountConfirmInvalid feishuCodexAccountConfirmState = iota
	feishuCodexAccountConfirmStarted
	feishuCodexAccountConfirmRunning
	feishuCodexAccountConfirmCompleted
)

// feishuCodexAccountConfirmStore 把确认能力绑定到机器人账号、操作者和 route。
// 已消费 token 保留到过期，以便重复点击返回同一个终态而不重复切换。
type feishuCodexAccountConfirmStore struct {
	mu      sync.Mutex
	records map[string]feishuCodexAccountConfirmation
	now     func() time.Time
}

func (s *feishuCodexAccountConfirmStore) issue(record feishuCodexAccountConfirmation) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowOrDefault()
	s.purgeExpiredLocked(now)
	if s.records == nil {
		s.records = make(map[string]feishuCodexAccountConfirmation)
	}
	token := feishuCodexAccountConfirmTokenPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	record.scope = normalizeFeishuCodexAccountConfirmScope(record.scope)
	record.previousLabel = strings.TrimSpace(record.previousLabel)
	record.targetLabel = strings.TrimSpace(record.targetLabel)
	record.expiresAt = now.Add(feishuCodexAccountConfirmTTL)
	s.records[token] = record
	return token
}

func (s *feishuCodexAccountConfirmStore) begin(token string, scope feishuCodexAccountConfirmScope) (feishuCodexAccountConfirmation, feishuCodexAccountConfirmState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowOrDefault()
	s.purgeExpiredLocked(now)
	token = strings.TrimSpace(token)
	record, ok := s.records[token]
	if !ok || record.scope != normalizeFeishuCodexAccountConfirmScope(scope) {
		return feishuCodexAccountConfirmation{}, feishuCodexAccountConfirmInvalid
	}
	if record.completed {
		return record, feishuCodexAccountConfirmCompleted
	}
	if record.consumed {
		return record, feishuCodexAccountConfirmRunning
	}
	record.consumed = true
	s.records[token] = record
	return record, feishuCodexAccountConfirmStarted
}

func (s *feishuCodexAccountConfirmStore) complete(token string, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[strings.TrimSpace(token)]
	if !ok {
		return
	}
	record.completed = true
	record.result = strings.TrimSpace(result)
	s.records[strings.TrimSpace(token)] = record
}

func (s *feishuCodexAccountConfirmStore) purgeExpiredLocked(now time.Time) {
	for token, record := range s.records {
		if !record.expiresAt.After(now) {
			delete(s.records, token)
		}
	}
}

func (s *feishuCodexAccountConfirmStore) nowOrDefault() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func normalizeFeishuCodexAccountConfirmScope(scope feishuCodexAccountConfirmScope) feishuCodexAccountConfirmScope {
	scope.AccountID = strings.TrimSpace(scope.AccountID)
	scope.ActorUserID = strings.TrimSpace(scope.ActorUserID)
	scope.RouteUserID = strings.TrimSpace(scope.RouteUserID)
	return scope
}
