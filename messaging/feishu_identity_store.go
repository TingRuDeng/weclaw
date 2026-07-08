package messaging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

const (
	feishuIdentityMaxPendingRecords      = 200
	feishuIdentityPendingTTL             = 30 * 24 * time.Hour
	feishuIdentityLastSeenUpdateInterval = time.Hour
)

type feishuIdentityStore struct {
	mu       sync.Mutex
	saveMu   sync.Mutex
	filePath string
	records  map[string]feishuIdentityRecord
	loadErr  error
}

// DefaultFeishuIdentityFile 返回飞书身份自动发现状态文件路径。
func DefaultFeishuIdentityFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".weclaw", "feishu-identities.json")
}

func newFeishuIdentityStore() *feishuIdentityStore {
	return &feishuIdentityStore{records: make(map[string]feishuIdentityRecord)}
}

// SetFilePath 设置身份状态文件路径，并加载已有自动发现记录。
func (s *feishuIdentityStore) SetFilePath(filePath string) {
	s.mu.Lock()
	s.filePath = strings.TrimSpace(filePath)
	s.mu.Unlock()
	s.load()
}

// Remember 记录飞书入站消息里的用户身份；这里只发现身份，不改变授权配置。
func (s *feishuIdentityStore) Remember(msg platform.IncomingMessage) {
	identity, ok := extractFeishuIdentity(msg)
	if !ok {
		return
	}
	nowTime := time.Now().UTC()
	s.mu.Lock()
	changed := s.upsertLocked(identity, nowTime)
	changed = s.purgeStalePendingLocked(nowTime) || changed
	changed = s.enforcePendingRecordLimitLocked() || changed
	s.mu.Unlock()
	if changed {
		s.save()
	}
}

func (s *feishuIdentityStore) ListPending() []feishuIdentityRecord {
	return s.listRecords(func(record feishuIdentityRecord) bool {
		return record.Pending && !record.Approved
	})
}

func (s *feishuIdentityStore) ListRecords() []feishuIdentityRecord {
	return s.listRecords(func(feishuIdentityRecord) bool { return true })
}

func (s *feishuIdentityStore) Approve(selector string) (feishuIdentityRecord, bool) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return feishuIdentityRecord{}, false
	}
	s.mu.Lock()
	key := s.resolveKeyLocked(selector)
	if key == "" {
		s.mu.Unlock()
		return feishuIdentityRecord{}, false
	}
	record := s.records[key]
	record.Approved = true
	record.Pending = false
	s.records[key] = record
	s.mu.Unlock()
	s.save()
	return copyFeishuIdentityRecord(record), true
}

func (s *feishuIdentityStore) Find(selector string) (feishuIdentityRecord, bool) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return feishuIdentityRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.resolveKeyLocked(selector)
	if key == "" {
		return feishuIdentityRecord{}, false
	}
	return copyFeishuIdentityRecord(s.records[key]), true
}

func (s *feishuIdentityStore) LoadError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

func (s *feishuIdentityStore) listRecords(keep func(feishuIdentityRecord) bool) []feishuIdentityRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]feishuIdentityRecord, 0, len(s.records))
	for _, record := range s.records {
		if keep(record) {
			records = append(records, copyFeishuIdentityRecord(record))
		}
	}
	sortFeishuIdentityRecords(records)
	return records
}

func (s *feishuIdentityStore) upsertLocked(identity feishuIdentityCandidate, nowTime time.Time) bool {
	now := nowTime.Format(time.RFC3339)
	record := s.records[identity.Key]
	previous := copyFeishuIdentityRecord(record)
	materialChanged := feishuIdentityHasNewMaterial(record, identity)
	if record.Key == "" {
		record = feishuIdentityRecord{Key: identity.Key, FirstSeen: now}
	}
	record = mergeOpenIDRecord(record, s.records, identity)
	record = applyFeishuIdentity(record, identity, now)
	if !materialChanged && !shouldRefreshFeishuIdentityLastSeen(previous.LastSeen, nowTime) {
		record.LastSeen = previous.LastSeen
	}
	s.records[record.Key] = record
	return materialChanged || record.LastSeen != previous.LastSeen
}

func feishuIdentityHasNewMaterial(record feishuIdentityRecord, identity feishuIdentityCandidate) bool {
	if record.Key == "" {
		return true
	}
	if identity.UnionID != "" && record.UnionID == "" {
		return true
	}
	if identity.UserID != "" && record.UserID == "" {
		return true
	}
	if identity.OpenID != "" && record.OpenID == "" {
		return true
	}
	if identity.AccountID != "" && identity.OpenID != "" && record.OpenIDs[identity.AccountID] != identity.OpenID {
		return true
	}
	return !stringSliceContains(record.Accounts, identity.AccountID)
}

func shouldRefreshFeishuIdentityLastSeen(lastSeen string, now time.Time) bool {
	seenAt, ok := parseFeishuIdentityTime(lastSeen)
	if !ok {
		return true
	}
	return now.Sub(seenAt) >= feishuIdentityLastSeenUpdateInterval
}

func (s *feishuIdentityStore) purgeStalePendingLocked(now time.Time) bool {
	changed := false
	for key, record := range s.records {
		if !record.Pending || record.Approved {
			continue
		}
		seenAt, ok := parseFeishuIdentityRecordTime(record)
		if ok && now.Sub(seenAt) > feishuIdentityPendingTTL {
			delete(s.records, key)
			changed = true
		}
	}
	return changed
}

func (s *feishuIdentityStore) enforcePendingRecordLimitLocked() bool {
	pending := make([]feishuIdentityRecord, 0, len(s.records))
	for _, record := range s.records {
		if record.Pending && !record.Approved {
			pending = append(pending, copyFeishuIdentityRecord(record))
		}
	}
	if len(pending) <= feishuIdentityMaxPendingRecords {
		return false
	}
	sortFeishuIdentityRecords(pending)
	for _, record := range pending[feishuIdentityMaxPendingRecords:] {
		delete(s.records, record.Key)
	}
	return true
}

func parseFeishuIdentityRecordTime(record feishuIdentityRecord) (time.Time, bool) {
	if t, ok := parseFeishuIdentityTime(record.LastSeen); ok {
		return t, true
	}
	return parseFeishuIdentityTime(record.FirstSeen)
}

func parseFeishuIdentityTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func stringSliceContains(values []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return true
	}
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
