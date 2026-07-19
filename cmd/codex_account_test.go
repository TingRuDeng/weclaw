package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/config"
)

type cmdAccountKeyring struct {
	mu     sync.Mutex
	values map[string]string
}

func (k *cmdAccountKeyring) Get(service, user string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	value, ok := k.values[service+"\x00"+user]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}
func (k *cmdAccountKeyring) Set(service, user, password string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.values[service+"\x00"+user] = password
	return nil
}
func (k *cmdAccountKeyring) Delete(service, user string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.values, service+"\x00"+user)
	return nil
}

func cmdAccountSnapshot(t *testing.T, email, accountID string) *codexauth.Snapshot {
	t.Helper()
	claims, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		t.Fatal(err)
	}
	idToken := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	data, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"access_token": "access-" + accountID, "refresh_token": "refresh-" + accountID,
			"id_token": idToken, "account_id": accountID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := codexauth.ParseSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func newCmdAccountStore(t *testing.T) (*codexauth.Store, codexauth.Profile, codexauth.Profile) {
	t.Helper()
	codexHome := filepath.Join(t.TempDir(), "codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := codexauth.NewStore(codexauth.StoreOptions{
		DataDir: filepath.Join(t.TempDir(), "weclaw"), CodexHome: codexHome,
		SocketPath: filepath.Join(t.TempDir(), "codex.sock"),
		Keyring:    &cmdAccountKeyring{values: make(map[string]string)},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldSnapshot := cmdAccountSnapshot(t, "old@example.com", "old")
	newSnapshot := cmdAccountSnapshot(t, "new@example.com", "new")
	if err := codexauth.WriteAuthFile(store.AuthPath(), oldSnapshot); err != nil {
		t.Fatal(err)
	}
	oldProfile, err := store.Save(context.Background(), oldSnapshot, codexauth.SaveOptions{Label: "旧账号"})
	if err != nil {
		t.Fatal(err)
	}
	var target codexauth.Profile
	if err := store.WithTransaction(context.Background(), func(tx *codexauth.Transaction) error {
		var putErr error
		target, putErr = tx.PutSnapshot(newSnapshot, codexauth.SaveOptions{Label: "新账号"})
		return putErr
	}); err != nil {
		t.Fatal(err)
	}
	return store, oldProfile, target
}

func TestUseOfflineCodexAccountProjectsAuthAndCommitsActiveProfile(t *testing.T) {
	store, _, target := newCmdAccountStore(t)
	status, err := store.Status()
	if err != nil {
		t.Fatal(err)
	}
	selected, changed, err := useOfflineCodexAccount(context.Background(), store, string(target.ID), status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || selected.ID != target.ID {
		t.Fatalf("selected=%#v changed=%v", selected, changed)
	}
	auth, err := codexauth.ReadAuthFile(store.AuthPath())
	if err != nil || !auth.MatchesEmail("new@example.com") {
		t.Fatalf("auth projection error=%v", err)
	}
	current, _, err := store.Current()
	if err != nil || current == nil || current.ID != target.ID {
		t.Fatalf("current=%#v error=%v", current, err)
	}
}

func TestUseOfflineCodexAccountRejectsStaleRevisionWithoutMutation(t *testing.T) {
	store, oldProfile, target := newCmdAccountStore(t)
	status, err := store.Status()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = useOfflineCodexAccount(context.Background(), store, string(target.ID), status.Revision+1)
	if codexauth.ErrorCode(err) != codexauth.CodeConflict {
		t.Fatalf("error=%v", err)
	}
	auth, authErr := codexauth.ReadAuthFile(store.AuthPath())
	current, _, currentErr := store.Current()
	if authErr != nil || !auth.MatchesEmail("old@example.com") || currentErr != nil || current == nil || current.ID != oldProfile.ID {
		t.Fatalf("stale revision mutated state: current=%#v authErr=%v currentErr=%v", current, authErr, currentErr)
	}
}

func TestOnlineCodexAccountStatusFailsClosedWhenAPIUnavailable(t *testing.T) {
	cfg := &config.Config{APIAddr: "127.0.0.1:1"}
	_, err := loadCodexAccountStatusWithRuntime(context.Background(), cfg, true, false)
	if err == nil || !strings.Contains(err.Error(), "已拒绝直接修改认证") {
		t.Fatalf("error=%v", err)
	}
}

func TestRuntimeAPIURLPreservesAccountQueryAndForcesLoopback(t *testing.T) {
	got, err := runtimeAPIURL("http://0.0.0.0:18011/base", "/api/codex/accounts/current?quota=1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:18011/api/codex/accounts/current?quota=1" {
		t.Fatalf("runtimeAPIURL=%q", got)
	}
}
