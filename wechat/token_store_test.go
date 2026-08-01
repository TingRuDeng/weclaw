package wechat

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/internal/accountstore"
)

func TestContextTokenStorePersistsAndLoads(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	store := newContextTokenStore("bot-1")
	if err := store.loadError(); err != nil {
		t.Fatalf("loadError=%v, want nil for missing token store", err)
	}

	if err := store.Set("user-1", "ctx-1"); err != nil {
		t.Fatalf("Set token error: %v", err)
	}
	loaded := newContextTokenStore("bot-1")
	if err := loaded.loadError(); err != nil {
		t.Fatalf("loadError=%v, want valid persisted store", err)
	}

	if got := loaded.Get("user-1"); got != "ctx-1" {
		t.Fatalf("loaded token=%q, want ctx-1", got)
	}
	data, err := os.ReadFile(contextTokenStorePath("bot-1"))
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if !strings.Contains(string(data), "wechat:user-1") {
		t.Fatalf("token file=%s, want platform-qualified key", string(data))
	}
}

func TestContextTokenStorePathConfinesUntrustedBotID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	accountsDir := filepath.Join(home, "accounts")
	for _, botID := range []string{"../escape", "/tmp/absolute", `..\windows\escape`, "bot/child"} {
		path := contextTokenStorePath(botID)
		rel, err := filepath.Rel(accountsDir, path)
		if err != nil {
			t.Fatal(err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			t.Fatalf("botID=%q escaped accounts dir: path=%q rel=%q", botID, path, rel)
		}
	}
}

func TestContextTokenStoreSerializesSnapshotAndWrite(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	first := newContextTokenStore("bot-serial")
	second := newContextTokenStore("bot-serial")
	start := make(chan struct{})
	errorsByUser := make(chan error, 2)
	go func() {
		<-start
		errorsByUser <- first.Set("user-1", "ctx-1")
	}()
	go func() {
		<-start
		errorsByUser <- second.Set("user-2", "ctx-2")
	}()
	close(start)
	for range 2 {
		if err := <-errorsByUser; err != nil {
			t.Fatalf("Set error: %v", err)
		}
	}
	loaded := newContextTokenStore("bot-serial")
	if got := loaded.Get("user-1"); got != "ctx-1" {
		t.Fatalf("loaded token=%q, want ctx-1", got)
	}
	if got := loaded.Get("user-2"); got != "ctx-2" {
		t.Fatalf("loaded token=%q, want ctx-2", got)
	}
}

func TestContextTokenStoreHonorsCrossProcessAccountLock(t *testing.T) {
	if os.Getenv("WECLAW_CONTEXT_TOKEN_LOCK_WAITER") == "1" {
		store := newContextTokenStore("bot-lock")
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := store.set(ctx, "user-1", "ctx-1")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("context token set error=%v, want deadline exceeded", err)
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("official context-token store assets are Unix-only")
	}

	t.Setenv("WECLAW_HOME", t.TempDir())
	dir := filepath.Dir(contextTokenStorePath("bot-lock"))
	if err := accountstore.WithWriteLock(context.Background(), dir, func() error {
		command := exec.Command(os.Args[0], "-test.run=^TestContextTokenStoreHonorsCrossProcessAccountLock$")
		command.Env = append(os.Environ(), "WECLAW_CONTEXT_TOKEN_LOCK_WAITER=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("cross-process token waiter error=%v output=%s", err, output)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store := newContextTokenStore("bot-lock")
	if err := store.Set("user-1", "ctx-1"); err != nil {
		t.Fatalf("Set after lock release: %v", err)
	}
}

func TestContextTokenStoreRejectsMalformedState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	path := contextTokenStorePath("bot-malformed")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create accounts dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write malformed token file: %v", err)
	}

	store := newContextTokenStore("bot-malformed")
	if err := store.loadError(); err == nil || !strings.Contains(err.Error(), "parse context token file") {
		t.Fatalf("loadError=%v, want malformed JSON error", err)
	}
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-malformed"})
	if err := adapter.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "load context token store") {
		t.Fatalf("Adapter.Run error=%v, want token store startup failure", err)
	}
}

func TestContextTokenStoreRejectsNullState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	path := contextTokenStorePath("bot-null")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create accounts dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatalf("write null token file: %v", err)
	}

	store := newContextTokenStore("bot-null")
	if err := store.loadError(); err == nil || !strings.Contains(err.Error(), "expected a JSON object") {
		t.Fatalf("loadError=%v, want null JSON rejection", err)
	}
}

func TestContextTokenStoreRejectsNormalizedBotIDCollision(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	first := newContextTokenStore("bot@example.com:1")
	if err := first.Set("user-1", "ctx-first"); err != nil {
		t.Fatalf("seed first account token: %v", err)
	}

	colliding := newContextTokenStore("bot-example-com-1")
	if err := colliding.loadError(); err == nil || !strings.Contains(err.Error(), "filename collision") {
		t.Fatalf("loadError=%v, want normalized bot ID collision", err)
	}
	if err := colliding.Set("user-1", "ctx-second"); err == nil {
		t.Fatal("Set error=nil, want normalized bot ID collision")
	}
	loaded := newContextTokenStore("bot@example.com:1")
	if got := loaded.Get("user-1"); got != "ctx-first" {
		t.Fatalf("first account token=%q, want ctx-first", got)
	}
}

func TestContextTokenStoreValidatesLegacyOwnerBeforeLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	if err := ilink.SaveCredentials(&ilink.Credentials{
		BotToken:   "token",
		ILinkBotID: "bot@example.com:1",
	}); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	path := contextTokenStorePath("bot@example.com:1")
	legacy := []byte(`{"wechat:user-1":"ctx-first"}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write legacy token state: %v", err)
	}

	matching := newContextTokenStore("bot@example.com:1")
	if err := matching.loadError(); err != nil {
		t.Fatalf("matching legacy loadError=%v", err)
	}
	if got := matching.Get("user-1"); got != "ctx-first" {
		t.Fatalf("matching legacy token=%q, want ctx-first", got)
	}
	colliding := newContextTokenStore("bot-example-com-1")
	if err := colliding.loadError(); err == nil || !strings.Contains(err.Error(), "filename collision") {
		t.Fatalf("colliding legacy loadError=%v, want filename collision", err)
	}
}

func TestContextTokenStoreWriteFailureKeepsCommittedMemoryState(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	store := newContextTokenStore("bot-rollback")
	if err := store.Set("user-1", "ctx-old"); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	if err := os.Remove(store.path); err != nil {
		t.Fatalf("remove token file: %v", err)
	}
	if err := os.Mkdir(store.path, 0o700); err != nil {
		t.Fatalf("replace token file with unsafe directory: %v", err)
	}

	if err := store.Set("user-1", "ctx-new"); err == nil {
		t.Fatal("Set error=nil, want secure persistence failure")
	}
	if got := store.Get("user-1"); got != "ctx-old" {
		t.Fatalf("memory token=%q, want last durably committed ctx-old", got)
	}
}

func TestAdapterNewReplierUsesPersistedContextToken(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	if err := adapter.tokenStore.Set("user-1", "ctx-1"); err != nil {
		t.Fatalf("Set token error: %v", err)
	}

	reply := adapter.NewReplier("user-1")
	wechatReply, ok := reply.(*Replier)
	if !ok {
		t.Fatalf("reply=%T, want *Replier", reply)
	}
	if wechatReply.ContextToken != "ctx-1" {
		t.Fatalf("ContextToken=%q, want persisted token", wechatReply.ContextToken)
	}
}
