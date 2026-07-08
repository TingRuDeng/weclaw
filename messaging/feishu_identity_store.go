package messaging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
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
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	s.upsertLocked(identity, now)
	s.mu.Unlock()
	s.save()
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

func (s *feishuIdentityStore) upsertLocked(identity feishuIdentityCandidate, now string) {
	record := s.records[identity.Key]
	if record.Key == "" {
		record = feishuIdentityRecord{Key: identity.Key, FirstSeen: now}
	}
	record = mergeOpenIDRecord(record, s.records, identity)
	record = applyFeishuIdentity(record, identity, now)
	s.records[record.Key] = record
}
