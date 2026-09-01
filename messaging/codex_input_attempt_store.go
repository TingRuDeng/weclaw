package messaging

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

const (
	codexInputAttemptStateVersion = 1
	codexInputAttemptHistoryLimit = 256
)

type codexInputAttemptRecord struct {
	AttemptID      string                        `json:"attemptId"`
	MessageKey     string                        `json:"messageKey,omitempty"`
	ThreadID       string                        `json:"threadId"`
	BaselineTurnID string                        `json:"baselineTurnId,omitempty"`
	ExpectedTurnID string                        `json:"expectedTurnId,omitempty"`
	MessageDigest  string                        `json:"messageDigest"`
	Status         agent.CodexInputAttemptStatus `json:"status"`
	UpdatedAt      string                        `json:"updatedAt"`
}

type codexInputAttemptState struct {
	Version  int                       `json:"version"`
	Attempts []codexInputAttemptRecord `json:"attempts"`
	Updated  string                    `json:"updated"`
}

type codexInputAttemptStore struct {
	mu       sync.Mutex
	filePath string
	records  map[string]codexInputAttemptRecord
}

func newCodexInputAttemptStore() *codexInputAttemptStore {
	return &codexInputAttemptStore{records: make(map[string]codexInputAttemptRecord)}
}

func codexInputAttemptFileForSession(sessionFile string) string {
	sessionFile = strings.TrimSpace(sessionFile)
	if sessionFile == "" {
		return ""
	}
	ext := filepath.Ext(sessionFile)
	base := strings.TrimSuffix(sessionFile, ext)
	return base + "-input-attempts.json"
}

func (s *codexInputAttemptStore) SetFilePath(filePath string) {
	s.mu.Lock()
	s.filePath = strings.TrimSpace(filePath)
	s.records = make(map[string]codexInputAttemptRecord)
	s.mu.Unlock()
	if err := s.load(); err != nil {
		log.Printf("[codex-input] 读取交付尝试元数据失败（不会自动重放输入）: %v", err)
	}
}

func (s *codexInputAttemptStore) record(attempt agent.CodexInputAttempt) error {
	record := codexInputAttemptRecord{
		AttemptID: strings.TrimSpace(attempt.AttemptID), MessageKey: strings.TrimSpace(attempt.MessageKey),
		ThreadID: strings.TrimSpace(attempt.ThreadID), BaselineTurnID: strings.TrimSpace(attempt.BaselineTurnID),
		ExpectedTurnID: strings.TrimSpace(attempt.ExpectedTurnID), MessageDigest: strings.TrimSpace(attempt.MessageDigest),
		Status: attempt.Status, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if record.AttemptID == "" || record.ThreadID == "" || record.MessageDigest == "" || record.Status == "" {
		return fmt.Errorf("Codex 输入交付尝试元数据不完整")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]codexInputAttemptRecord)
	}
	s.records[record.AttemptID] = record
	s.pruneLocked()
	return s.saveLocked()
}

func (s *codexInputAttemptStore) snapshot() []codexInputAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedCodexInputAttemptRecords(s.records)
}

func (s *codexInputAttemptStore) pruneLocked() {
	if len(s.records) <= codexInputAttemptHistoryLimit {
		return
	}
	records := sortedCodexInputAttemptRecords(s.records)
	for _, record := range records[:len(records)-codexInputAttemptHistoryLimit] {
		delete(s.records, record.AttemptID)
	}
}

func (s *codexInputAttemptStore) saveLocked() error {
	if s.filePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o700); err != nil {
		return fmt.Errorf("创建 Codex 输入交付状态目录: %w", err)
	}
	state := codexInputAttemptState{
		Version: codexInputAttemptStateVersion, Attempts: sortedCodexInputAttemptRecords(s.records),
		Updated: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 Codex 输入交付状态: %w", err)
	}
	return writeAgentSessionStateAtomically(s.filePath, data)
}

func (s *codexInputAttemptStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.filePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.filePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state codexInputAttemptState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Version != codexInputAttemptStateVersion {
		return fmt.Errorf("不支持的 Codex 输入交付状态版本: %d", state.Version)
	}
	for _, record := range state.Attempts {
		if record.AttemptID != "" {
			s.records[record.AttemptID] = record
		}
	}
	s.pruneLocked()
	return nil
}

func sortedCodexInputAttemptRecords(records map[string]codexInputAttemptRecord) []codexInputAttemptRecord {
	result := make([]codexInputAttemptRecord, 0, len(records))
	for _, record := range records {
		result = append(result, record)
	}
	sort.Slice(result, func(i int, j int) bool {
		if result[i].UpdatedAt == result[j].UpdatedAt {
			return result[i].AttemptID < result[j].AttemptID
		}
		return result[i].UpdatedAt < result[j].UpdatedAt
	})
	return result
}
