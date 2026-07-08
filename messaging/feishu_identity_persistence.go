package messaging

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

func (s *feishuIdentityStore) load() {
	s.mu.Lock()
	filePath := s.filePath
	s.loadErr = nil
	s.mu.Unlock()
	if filePath == "" {
		return
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		s.setLoadErrorUnlessMissing(filePath, err)
		return
	}
	var state feishuIdentityState
	if err := json.Unmarshal(data, &state); err != nil {
		s.setLoadError(filePath, err)
		return
	}
	s.mu.Lock()
	s.records = normalizeFeishuIdentityRecords(state.Records)
	s.mu.Unlock()
}

func normalizeFeishuIdentityRecords(records map[string]feishuIdentityRecord) map[string]feishuIdentityRecord {
	normalized := make(map[string]feishuIdentityRecord, len(records))
	for key, record := range records {
		record.Key = firstNonBlank(record.Key, key)
		if record.Key == "" {
			continue
		}
		record.Pending = record.Pending && !record.Approved
		normalized[record.Key] = copyFeishuIdentityRecord(record)
	}
	return normalized
}

func (s *feishuIdentityStore) setLoadErrorUnlessMissing(filePath string, err error) {
	if os.IsNotExist(err) {
		return
	}
	s.setLoadError(filePath, err)
}

func (s *feishuIdentityStore) setLoadError(filePath string, err error) {
	log.Printf("[feishu-identity] failed to load %s: %v", filePath, err)
	s.mu.Lock()
	s.loadErr = err
	s.mu.Unlock()
}

func (s *feishuIdentityStore) save() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	state, filePath := s.snapshot()
	if filePath == "" {
		return
	}
	if err := writeFeishuIdentityState(filePath, state); err != nil {
		log.Printf("[feishu-identity] failed to save %s: %v", filePath, err)
	}
}

func (s *feishuIdentityStore) snapshot() (feishuIdentityState, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := feishuIdentityState{
		Version: feishuIdentityStoreVersion,
		Records: make(map[string]feishuIdentityRecord, len(s.records)),
		Updated: time.Now().UTC().Format(time.RFC3339),
	}
	for key, record := range s.records {
		state.Records[key] = copyFeishuIdentityRecord(record)
	}
	return state, s.filePath
}

func writeFeishuIdentityState(filePath string, state feishuIdentityState) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic0600(filePath, data)
}

func writeAtomic0600(filePath string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(filePath), filepath.Base(filePath)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filePath)
}
