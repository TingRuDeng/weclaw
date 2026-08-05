package ilink

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

	"github.com/fastclaw-ai/weclaw/internal/accountstore"
)

type failingQRStatusClient struct {
	calls int
}

func (c *failingQRStatusClient) doGet(context.Context, string, any) error {
	c.calls++
	return errors.New("network unavailable")
}

func TestPollQRStatusBacksOffImmediateTransportErrors(t *testing.T) {
	client := &failingQRStatusClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()

	_, err := pollQRStatus(ctx, client, "https://example.invalid/status", nil)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pollQRStatus() error=%v, want deadline", err)
	}
	if client.calls != 1 {
		t.Fatalf("transport calls=%d, immediate errors should back off before retry", client.calls)
	}
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("poll returned too quickly without cancellable backoff: %s", elapsed)
	}
}

func TestNormalizeAccountIDIsFilesystemSafe(t *testing.T) {
	for _, raw := range []string{"../escape", "/tmp/absolute", `..\windows\escape`, "bot/child", "机器人/一"} {
		got := NormalizeAccountID(raw)
		if got == "" || got == "." || got == ".." || filepath.IsAbs(got) || strings.ContainsAny(got, `/\\`) {
			t.Fatalf("NormalizeAccountID(%q)=%q", raw, got)
		}
	}
	if got := NormalizeAccountID("bot@example.com:1"); got != "bot-example-com-1" {
		t.Fatalf("compatibility normalization=%q", got)
	}
}

func TestLoadAllCredentialsRejectsMissingBotID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	accountsDir := filepath.Join(home, "accounts")
	if err := os.MkdirAll(accountsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(accountsDir, "invalid.json"), []byte(`{"bot_token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(accountsDir, "valid.json"), []byte(`{"bot_token":"secret","ilink_bot_id":"bot-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	accounts, err := LoadAllCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ILinkBotID != "bot-1" {
		t.Fatalf("accounts=%#v, want only bot-1", accounts)
	}
}

func TestLoadAllCredentialsReturnsEmptyWhenAccountsDirIsMissing(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())

	accounts, err := LoadAllCredentials()

	if err != nil {
		t.Fatalf("LoadAllCredentials error: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts=%#v, want empty result", accounts)
	}
}

func TestSaveCredentialsRejectsNil(t *testing.T) {
	if err := SaveCredentials(nil); err == nil {
		t.Fatal("SaveCredentials(nil) should fail")
	}
}

func TestSaveCredentialsCreatesFirstAccountFile(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	creds := &Credentials{BotToken: "token", ILinkBotID: "bot-1"}
	if err := SaveCredentials(creds); err != nil {
		t.Fatalf("SaveCredentials first account: %v", err)
	}
	accounts, err := LoadAllCredentials()
	if err != nil {
		t.Fatalf("LoadAllCredentials: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ILinkBotID != creds.ILinkBotID {
		t.Fatalf("accounts=%#v, want bot-1", accounts)
	}
}

func TestSaveCredentialsSerializesNormalizedBotIDCollision(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	start := make(chan struct{})
	errorsByID := make(chan error, 2)
	for _, botID := range []string{"bot@example.com:1", "bot-example-com-1"} {
		botID := botID
		go func() {
			<-start
			errorsByID <- SaveCredentials(&Credentials{BotToken: "token", ILinkBotID: botID})
		}()
	}
	close(start)
	firstErr := <-errorsByID
	secondErr := <-errorsByID
	if firstErr == nil && secondErr == nil {
		t.Fatal("both colliding credential saves succeeded")
	}
	if firstErr != nil && secondErr != nil {
		t.Fatalf("both colliding credential saves failed: first=%v second=%v", firstErr, secondErr)
	}
	accounts, err := LoadAllCredentials()
	if err != nil {
		t.Fatalf("LoadAllCredentials: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts=%#v, want one collision winner", accounts)
	}
}

func TestSaveCredentialsHonorsCrossProcessAccountLock(t *testing.T) {
	if os.Getenv("WECLAW_ILINK_SAVE_LOCK_WAITER") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := saveCredentials(ctx, &Credentials{BotToken: "token", ILinkBotID: "bot-lock"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("saveCredentials error=%v, want deadline exceeded", err)
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("official credential-store assets are Unix-only")
	}

	t.Setenv("WECLAW_HOME", t.TempDir())
	dir, err := AccountsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := accountstore.WithWriteLock(context.Background(), dir, func() error {
		command := exec.Command(os.Args[0], "-test.run=^TestSaveCredentialsHonorsCrossProcessAccountLock$")
		command.Env = append(os.Environ(), "WECLAW_ILINK_SAVE_LOCK_WAITER=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("cross-process save waiter error=%v output=%s", err, output)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredentials(&Credentials{BotToken: "token", ILinkBotID: "bot-lock"}); err != nil {
		t.Fatalf("SaveCredentials after lock release: %v", err)
	}
}

func TestSaveCredentialsRepairsExistingFilePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	accountsDir := filepath.Join(home, "accounts")
	if err := os.MkdirAll(accountsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(accountsDir, "bot-1.json")
	if err := os.WriteFile(path, []byte(`{"bot_token":"old","ilink_bot_id":"bot-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SaveCredentials(&Credentials{BotToken: "new", ILinkBotID: "bot-1"}); err != nil {
		t.Fatalf("SaveCredentials error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credentials mode=%o, want 600", mode)
	}
}

func TestLoadAllCredentialsRejectsInsecureFilePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	accountsDir := filepath.Join(home, "accounts")
	if err := os.MkdirAll(accountsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(accountsDir, "bot-1.json")
	if err := os.WriteFile(path, []byte(`{"bot_token":"secret","ilink_bot_id":"bot-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadAllCredentials(); err == nil {
		t.Fatal("LoadAllCredentials should reject credentials readable by other users")
	}
}

func TestCredentialsRejectSymlinkTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	accountsDir := filepath.Join(home, "accounts")
	if err := os.MkdirAll(accountsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "outside.json")
	original := []byte(`{"bot_token":"unchanged","ilink_bot_id":"bot-1"}`)
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(accountsDir, "bot-1.json")); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadAllCredentials(); err == nil {
		t.Fatal("LoadAllCredentials should reject a symlink credentials path")
	}
	if err := SaveCredentials(&Credentials{BotToken: "new", ILinkBotID: "bot-1"}); err == nil {
		t.Fatal("SaveCredentials should reject a symlink credentials path")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("symlink target changed: %q", data)
	}
}

func TestCredentialsRejectNormalizedBotIDCollision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	accountsDir := filepath.Join(home, "accounts")
	if err := os.MkdirAll(accountsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{"bot_token":"first","ilink_bot_id":"bot@example.com:1"}`)
	if err := os.WriteFile(filepath.Join(accountsDir, "bot-example-com-1.json"), existing, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredentials(&Credentials{BotToken: "second", ILinkBotID: "bot-example-com-1"}); err == nil {
		t.Fatal("SaveCredentials should reject a normalized bot ID collision")
	}
	duplicate := []byte(`{"bot_token":"second","ilink_bot_id":"bot-example-com-1"}`)
	if err := os.WriteFile(filepath.Join(accountsDir, "manually-copied.json"), duplicate, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAllCredentials(); err == nil {
		t.Fatal("LoadAllCredentials should reject a normalized bot ID collision")
	}
}
