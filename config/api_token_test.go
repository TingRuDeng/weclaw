package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type failingTokenReader struct{ err error }

func (r failingTokenReader) Read([]byte) (int, error) { return 0, r.err }

func TestEnsureAPITokenGeneratesAndPersistsPrivateToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)

	token, err := ensureAPITokenWithReader(strings.NewReader(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatalf("ensureAPITokenWithReader: %v", err)
	}
	if len(token) != 43 || strings.Contains(token, "=") {
		t.Fatalf("token format length=%d padding=%t", len(token), strings.Contains(token, "="))
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.APIToken != token {
		t.Fatalf("persisted token mismatch")
	}
	info, err := os.Stat(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode=%#o, want 0600", info.Mode().Perm())
	}
}

func TestEnsureAPITokenMigratesLegacyEmptyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	if err := Save(DefaultConfig()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	token, err := ensureAPITokenWithReader(strings.NewReader(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatalf("ensureAPITokenWithReader: %v", err)
	}
	if strings.TrimSpace(token) == "" {
		t.Fatal("legacy empty config was not migrated")
	}
}

func TestEnsureAPITokenKeepsExistingValueWithoutReadingRandomSource(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	cfg := DefaultConfig()
	cfg.APIToken = "existing-token"
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	token, err := ensureAPITokenWithReader(failingTokenReader{err: errors.New("must not read")})
	if err != nil {
		t.Fatalf("ensureAPITokenWithReader: %v", err)
	}
	if token != "existing-token" {
		t.Fatalf("token=%q, want existing token", token)
	}
}

func TestEnsureAPITokenIsIdempotentAndConcurrent(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	const workers = 16
	tokens := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := EnsureAPIToken()
			tokens <- token
			errs <- err
		}()
	}
	wg.Wait()
	close(tokens)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("EnsureAPIToken: %v", err)
		}
	}
	var want string
	for token := range tokens {
		if want == "" {
			want = token
		}
		if token != want {
			t.Fatalf("concurrent tokens differ")
		}
	}
}

func TestEnsureAPITokenRandomFailureDoesNotWriteConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	wantErr := errors.New("random unavailable")

	if _, err := ensureAPITokenWithReader(failingTokenReader{err: wantErr}); !errors.Is(err, wantErr) {
		t.Fatalf("ensureAPITokenWithReader error=%v, want %v", err, wantErr)
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config file after random failure: %v", err)
	}
}

func TestEnsureAPITokenDoesNotPersistEnvironmentOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	t.Setenv("WECLAW_API_TOKEN", "environment-token")

	token, err := ensureAPITokenWithReader(strings.NewReader(strings.Repeat("c", 32)))
	if err != nil {
		t.Fatalf("ensureAPITokenWithReader: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var persisted Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if persisted.APIToken != token || persisted.APIToken == "environment-token" {
		t.Fatalf("persisted api_token used environment override")
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.APIToken != "environment-token" {
		t.Fatalf("runtime token=%q, want environment override", loaded.APIToken)
	}
}
