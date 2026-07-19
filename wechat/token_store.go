package wechat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
)

type contextTokenStore struct {
	mu     sync.RWMutex
	saveMu sync.Mutex
	path   string
	tokens map[string]string
}

// newContextTokenStore 加载指定微信账号的 context_token 文件，供主动发送复用。
func newContextTokenStore(botID string) *contextTokenStore {
	store := &contextTokenStore{
		path:   contextTokenStorePath(botID),
		tokens: make(map[string]string),
	}
	store.load()
	return store
}

func contextTokenStorePath(botID string) string {
	home, _ := config.DataDir()
	accountID := ilink.NormalizeAccountID(botID)
	if accountID == "" {
		accountID = "invalid-account"
	}
	return filepath.Join(home, "accounts", accountID+".tokens.json")
}

func (s *contextTokenStore) Get(userID string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tokens[tokenKey(userID)]
}

func (s *contextTokenStore) Set(userID string, token string) error {
	if s == nil || strings.TrimSpace(userID) == "" || strings.TrimSpace(token) == "" {
		return nil
	}
	s.mu.Lock()
	s.tokens[tokenKey(userID)] = token
	s.mu.Unlock()

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.RLock()
	snapshot := make(map[string]string, len(s.tokens))
	for key, value := range s.tokens {
		snapshot[key] = value
	}
	s.mu.RUnlock()
	return writeContextTokens(s.path, snapshot)
}

func (s *contextTokenStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &s.tokens)
}

func writeContextTokens(path string, tokens map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context tokens: %w", err)
	}
	return replaceContextTokenFile(path, data)
}

func replaceContextTokenFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tokens-*.tmp")
	if err != nil {
		return fmt.Errorf("create token temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := writeContextTokenTemp(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace token file: %w", err)
	}
	return nil
}

func writeContextTokenTemp(tmp *os.File, data []byte) error {
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod token temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write token temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync token temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close token temp file: %w", err)
	}
	return nil
}

func tokenKey(userID string) string {
	return string(platform.PlatformWeChat) + ":" + strings.TrimSpace(userID)
}
