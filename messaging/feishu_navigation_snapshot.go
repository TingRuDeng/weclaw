package messaging

import (
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const (
	feishuNavigationSnapshotTTL         = 5 * time.Minute
	feishuNavigationSnapshotTokenPrefix = "@nav_"
	feishuNavigationSectionWorkspaces   = "workspaces"
	feishuNavigationSectionSessions     = "sessions"
	feishuNavigationSectionAccounts     = "accounts"
)

type feishuNavigationSnapshotScope struct {
	AccountID     string
	ActorUserID   string
	BindingKey    string
	AgentKind     string
	Section       string
	WorkspaceRoot string
}

type feishuNavigationSnapshotRecord struct {
	scope     feishuNavigationSnapshotScope
	choices   []platform.Choice
	expiresAt time.Time
}

// feishuNavigationSnapshotStore 保存一次列表打开时的稳定顺序，翻页不再重复查询目录。
type feishuNavigationSnapshotStore struct {
	mu      sync.Mutex
	records map[string]feishuNavigationSnapshotRecord
	now     func() time.Time
}

func (s *feishuNavigationSnapshotStore) issue(scope feishuNavigationSnapshotScope, choices []platform.Choice) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowOrDefault()
	s.purgeExpiredLocked(now)
	if s.records == nil {
		s.records = make(map[string]feishuNavigationSnapshotRecord)
	}
	token := feishuNavigationSnapshotTokenPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	s.records[token] = feishuNavigationSnapshotRecord{
		scope:     normalizeFeishuNavigationSnapshotScope(scope),
		choices:   clonePlatformChoices(choices),
		expiresAt: now.Add(feishuNavigationSnapshotTTL),
	}
	return token
}

func (s *feishuNavigationSnapshotStore) load(token string, scope feishuNavigationSnapshotScope) ([]platform.Choice, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowOrDefault()
	s.purgeExpiredLocked(now)
	record, ok := s.records[strings.TrimSpace(token)]
	if !ok || record.scope != normalizeFeishuNavigationSnapshotScope(scope) {
		return nil, false
	}
	return clonePlatformChoices(record.choices), true
}

func (s *feishuNavigationSnapshotStore) purgeExpiredLocked(now time.Time) {
	for token, record := range s.records {
		if !record.expiresAt.After(now) {
			delete(s.records, token)
		}
	}
}

func (s *feishuNavigationSnapshotStore) nowOrDefault() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func normalizeFeishuNavigationSnapshotScope(scope feishuNavigationSnapshotScope) feishuNavigationSnapshotScope {
	scope.AccountID = strings.TrimSpace(scope.AccountID)
	scope.ActorUserID = strings.TrimSpace(scope.ActorUserID)
	scope.BindingKey = strings.TrimSpace(scope.BindingKey)
	scope.AgentKind = strings.TrimSpace(scope.AgentKind)
	scope.Section = strings.TrimSpace(scope.Section)
	scope.WorkspaceRoot = strings.TrimSpace(scope.WorkspaceRoot)
	return scope
}

func clonePlatformChoices(choices []platform.Choice) []platform.Choice {
	cloned := make([]platform.Choice, 0, len(choices))
	for _, choice := range choices {
		choice.Metadata = mergeChoiceMetadata(choice.Metadata, nil)
		cloned = append(cloned, choice)
	}
	return cloned
}

func feishuNavigationSnapshotFromMessage(msg platform.IncomingMessage) string {
	if msg.RawCommand == nil {
		return ""
	}
	return strings.TrimSpace(msg.RawCommand.Value[platform.ChoiceMetadataNavigationSnapshot])
}
