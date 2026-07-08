package messaging

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	feishuIdentityAuthCodeTTL    = 30 * time.Minute
	feishuIdentityAuthCodeDigits = 6
)

// IssueAuthCode 为待授权身份生成短期授权码；已有未过期授权码时复用。
func (s *feishuIdentityStore) IssueAuthCode(selector string, now time.Time) (feishuIdentityRecord, bool) {
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
	if feishuAuthCodeValid(record, now) {
		s.mu.Unlock()
		return copyFeishuIdentityRecord(record), true
	}
	code, ok := s.newUniqueAuthCodeLocked(now)
	if !ok {
		s.mu.Unlock()
		return feishuIdentityRecord{}, false
	}
	record.AuthCode = code
	record.AuthCodeExpiresAt = now.Add(feishuIdentityAuthCodeTTL).UTC().Format(time.RFC3339)
	s.records[key] = record
	s.mu.Unlock()
	s.save()
	return copyFeishuIdentityRecord(record), true
}

func (s *feishuIdentityStore) newUniqueAuthCodeLocked(now time.Time) (string, bool) {
	for i := 0; i < 10; i++ {
		code, ok := randomFeishuAuthCode()
		if ok && !s.authCodeExistsLocked(code, now) {
			return code, true
		}
	}
	return "", false
}

func randomFeishuAuthCode() (string, bool) {
	limit := big.NewInt(1_000_000)
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%0*d", feishuIdentityAuthCodeDigits, value.Int64()), true
}

func (s *feishuIdentityStore) authCodeExistsLocked(code string, now time.Time) bool {
	for _, record := range s.records {
		if record.AuthCode == code && feishuAuthCodeValid(record, now) {
			return true
		}
	}
	return false
}

func (s *feishuIdentityStore) FindByAuthCode(code string, now time.Time) (feishuIdentityRecord, bool) {
	code = strings.TrimSpace(code)
	if code == "" {
		return feishuIdentityRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.AuthCode == code && feishuAuthCodeValid(record, now) {
			return copyFeishuIdentityRecord(record), true
		}
	}
	return feishuIdentityRecord{}, false
}

func feishuAuthCodeValid(record feishuIdentityRecord, now time.Time) bool {
	if strings.TrimSpace(record.AuthCode) == "" || strings.TrimSpace(record.AuthCodeExpiresAt) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, record.AuthCodeExpiresAt)
	if err != nil {
		return false
	}
	return now.Before(expiresAt)
}
