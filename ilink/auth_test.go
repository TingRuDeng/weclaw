package ilink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestSaveCredentialsRejectsNil(t *testing.T) {
	if err := SaveCredentials(nil); err == nil {
		t.Fatal("SaveCredentials(nil) should fail")
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
