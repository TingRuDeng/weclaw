package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/internal/accountstore"
	"github.com/fastclaw-ai/weclaw/internal/securefile"
	"github.com/fastclaw-ai/weclaw/platform"
)

type contextTokenStore struct {
	mu      sync.RWMutex
	botID   string
	path    string
	tokens  map[string]string
	loadErr error
}

const (
	contextTokenOwnerKey     = "_weclaw_bot_id"
	contextTokenWriteTimeout = 5 * time.Second
)

// newContextTokenStore 加载指定微信账号的 context_token 文件，供主动发送复用。
func newContextTokenStore(botID string) *contextTokenStore {
	path, err := resolveContextTokenStorePath(botID)
	store := &contextTokenStore{
		botID:  strings.TrimSpace(botID),
		path:   path,
		tokens: make(map[string]string),
	}
	if err != nil {
		store.loadErr = fmt.Errorf("resolve context token store path: %w", err)
		return store
	}
	store.loadErr = store.load()
	return store
}

func contextTokenStorePath(botID string) string {
	path, _ := resolveContextTokenStorePath(botID)
	return path
}

func resolveContextTokenStorePath(botID string) (string, error) {
	home, err := config.DataDir()
	if err != nil {
		return "", err
	}
	rawBotID := strings.TrimSpace(botID)
	accountID := ilink.NormalizeAccountID(rawBotID)
	if accountID == "" {
		return "", fmt.Errorf("empty bot id")
	}
	return filepath.Join(home, "accounts", accountID+".tokens.json"), nil
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
	ctx, cancel := context.WithTimeout(context.Background(), contextTokenWriteTimeout)
	defer cancel()
	return s.set(ctx, userID, token)
}

func (s *contextTokenStore) SetContext(ctx context.Context, userID string, token string) error {
	if ctx == nil {
		return fmt.Errorf("persist context token: nil context")
	}
	writeCtx, cancel := context.WithTimeout(ctx, contextTokenWriteTimeout)
	defer cancel()
	return s.set(writeCtx, userID, token)
}

func (s *contextTokenStore) set(ctx context.Context, userID string, token string) error {
	if s == nil || strings.TrimSpace(userID) == "" || strings.TrimSpace(token) == "" {
		return nil
	}
	s.mu.RLock()
	loadErr := s.loadErr
	s.mu.RUnlock()
	if loadErr != nil {
		return fmt.Errorf("context token store is unavailable: %w", loadErr)
	}
	return accountstore.WithWriteLock(ctx, filepath.Dir(s.path), func() error {
		s.mu.RLock()
		snapshot := make(map[string]string, len(s.tokens))
		for key, value := range s.tokens {
			snapshot[key] = value
		}
		s.mu.RUnlock()
		if diskTokens, err := readContextTokens(s.path, s.botID); err == nil {
			snapshot = diskTokens
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		snapshot[tokenKey(userID)] = token
		snapshot[contextTokenOwnerKey] = s.botID
		if err := writeContextTokens(s.path, snapshot); err != nil {
			return err
		}
		s.mu.Lock()
		s.tokens = snapshot
		s.mu.Unlock()
		return nil
	})
}

func (s *contextTokenStore) loadError() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadErr
}

func (s *contextTokenStore) load() error {
	loaded, err := readContextTokens(s.path, s.botID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	s.mu.Lock()
	s.tokens = loaded
	s.mu.Unlock()
	return nil
}

func readContextTokens(path string, botID string) (map[string]string, error) {
	data, err := securefile.Read(path)
	if err != nil {
		return nil, fmt.Errorf("read context token file %q: %w", path, err)
	}
	loaded := make(map[string]string)
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, fmt.Errorf("parse context token file %q: %w", path, err)
	}
	if loaded == nil {
		return nil, fmt.Errorf("parse context token file %q: expected a JSON object", path)
	}
	owner, hasOwner := loaded[contextTokenOwnerKey]
	if hasOwner {
		if strings.TrimSpace(owner) == "" || strings.TrimSpace(owner) != strings.TrimSpace(botID) {
			return nil, fmt.Errorf("context token bot id filename collision for %q", ilink.NormalizeAccountID(botID))
		}
		return loaded, nil
	}
	if err := validateLegacyContextTokenOwner(path, botID); err != nil {
		return nil, err
	}
	loaded[contextTokenOwnerKey] = strings.TrimSpace(botID)
	return loaded, nil
}

func validateLegacyContextTokenOwner(tokenPath string, botID string) error {
	accounts, err := ilink.LoadAllCredentials()
	if err != nil {
		return fmt.Errorf("validate legacy context token owner for %q: %w", tokenPath, err)
	}
	wantedID := strings.TrimSpace(botID)
	wantedFilename := ilink.NormalizeAccountID(wantedID)
	for _, creds := range accounts {
		if creds == nil || ilink.NormalizeAccountID(creds.ILinkBotID) != wantedFilename {
			continue
		}
		if strings.TrimSpace(creds.ILinkBotID) != wantedID {
			return fmt.Errorf("context token bot id filename collision for %q", wantedFilename)
		}
		return nil
	}
	return fmt.Errorf("validate legacy context token owner for %q: matching credentials not found", tokenPath)
}

func writeContextTokens(path string, tokens map[string]string) error {
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context tokens: %w", err)
	}
	if err := securefile.Write(path, data); err != nil {
		return fmt.Errorf("write context token file: %w", err)
	}
	return nil
}

func tokenKey(userID string) string {
	return string(platform.PlatformWeChat) + ":" + strings.TrimSpace(userID)
}
