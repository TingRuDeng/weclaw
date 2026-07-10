package wechat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

type contextTokenStore struct {
	mu     sync.RWMutex
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
	return filepath.Join(home, "accounts", strings.TrimSpace(botID)+".tokens.json")
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
	snapshot := make(map[string]string, len(s.tokens))
	for key, value := range s.tokens {
		snapshot[key] = value
	}
	s.mu.Unlock()
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
	return os.WriteFile(path, data, 0o600)
}

func tokenKey(userID string) string {
	return string(platform.PlatformWeChat) + ":" + strings.TrimSpace(userID)
}
