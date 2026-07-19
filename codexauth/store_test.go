package codexauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeKeyring struct {
	mu     sync.Mutex
	values map[string]string
	setErr error
}

func newFakeKeyring() *fakeKeyring {
	return &fakeKeyring{values: make(map[string]string)}
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.values[service+"\x00"+user]
	if !ok {
		return "", errSecretNotFound
	}
	return value, nil
}

func (f *fakeKeyring) Set(service, user, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.values[service+"\x00"+user] = password
	return nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, service+"\x00"+user)
	return nil
}

func newTestStore(t *testing.T, keyring KeyringClient) *Store {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "weclaw")
	codexHome := filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), testAuthJSON(t, "alice@example.com", "acct-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(StoreOptions{
		DataDir:    dataDir,
		CodexHome:  codexHome,
		SocketPath: filepath.Join(t.TempDir(), "app.sock"),
		Keyring:    keyring,
		Now: func() time.Time {
			return time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestStoreSaveListReplaceAndRemove(t *testing.T) {
	keyring := newFakeKeyring()
	store := newTestStore(t, keyring)
	ctx := context.Background()

	profile, err := store.SaveAuthFile(ctx, SaveOptions{Label: "工作账号"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Current == nil || status.Current.ID != profile.ID || len(status.Profiles) != 1 || status.Revision != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	indexInfo, err := os.Stat(filepath.Join(store.Root(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if indexInfo.Mode().Perm() != 0o600 {
		t.Fatalf("index permissions=%o", indexInfo.Mode().Perm())
	}
	rootInfo, err := os.Stat(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	if rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("root permissions=%o", rootInfo.Mode().Perm())
	}

	if _, err := store.SaveAuthFile(ctx, SaveOptions{Label: "工作账号"}); ErrorCode(err) != CodeConflict {
		t.Fatalf("duplicate save error=%v", err)
	}
	replaced, err := store.SaveAuthFile(ctx, SaveOptions{Label: "工作账号", Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.ID != profile.ID {
		t.Fatalf("replace changed profile id: %q -> %q", profile.ID, replaced.ID)
	}
	if err := store.Remove(ctx, string(profile.ID)); ErrorCode(err) != CodeConflict {
		t.Fatalf("remove active error=%v", err)
	}
}

func TestStoreFileFallbackRequiresExplicitConsent(t *testing.T) {
	keyring := newFakeKeyring()
	keyring.setErr = errors.New("keyring unavailable")
	store := newTestStore(t, keyring)

	if _, err := store.SaveAuthFile(context.Background(), SaveOptions{Label: "fallback"}); ErrorCode(err) != CodeFileStoreConsentRequired {
		t.Fatalf("SaveAuthFile() error=%v", err)
	}
	profile, err := store.SaveAuthFile(context.Background(), SaveOptions{Label: "fallback", AllowFileStore: true})
	if err != nil {
		t.Fatal(err)
	}
	if profile.SecretBackend != SecretBackendFile {
		t.Fatalf("secret backend=%q", profile.SecretBackend)
	}
	secretPath := filepath.Join(store.Root(), "secrets", profile.SecretRef+".json")
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret permissions=%o", info.Mode().Perm())
	}
	_, snapshot, err := store.ReadProfileSecret(context.Background(), profile.Label)
	if err != nil || !snapshot.MatchesEmail("alice@example.com") {
		t.Fatalf("ReadProfileSecret() snapshot=%v error=%v", snapshot != nil, err)
	}
}

func TestStoreRejectsCorruptAndSymlinkedIndex(t *testing.T) {
	store := newTestStore(t, newFakeKeyring())
	if err := os.MkdirAll(store.Root(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), "index.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); ErrorCode(err) != CodeInvalid {
		t.Fatalf("corrupt index error=%v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Remove(filepath.Join(store.Root(), "index.json")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"profiles":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store.Root(), "index.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "账号读取索引失败") {
		t.Fatalf("symlink index error=%v", err)
	}
}

func TestStoreRejectsIndexWithAbnormalPermissions(t *testing.T) {
	store := newTestStore(t, newFakeKeyring())
	if err := os.MkdirAll(store.Root(), 0o700); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(store.Root(), "index.json")
	if err := os.WriteFile(indexPath, []byte(`{"version":1,"profiles":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(indexPath, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); ErrorCode(err) != CodeInvalid {
		t.Fatalf("abnormal index permissions error=%v", err)
	}
}

func TestAccountStoreLockHonorsContext(t *testing.T) {
	store := newTestStore(t, newFakeKeyring())
	if err := ensureSecureDir(store.Root()); err != nil {
		t.Fatal(err)
	}
	first, err := acquireFileLock(context.Background(), filepath.Join(store.Root(), "switch.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	_, err = acquireFileLock(ctx, filepath.Join(store.Root(), "switch.lock"))
	if ErrorCode(err) != CodeBusy {
		t.Fatalf("second lock error=%v", err)
	}
}

func TestAccountStoreLockSerializesAcrossProcesses(t *testing.T) {
	if os.Getenv("WECLAW_CODEXAUTH_LOCK_HELPER") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, err := acquireFileLock(ctx, os.Getenv("WECLAW_CODEXAUTH_LOCK_PATH")); ErrorCode(err) != CodeBusy {
			t.Fatalf("helper lock error=%v", err)
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("official account-switch assets are Unix-only")
	}
	store := newTestStore(t, newFakeKeyring())
	if err := store.ensureStoreRoot(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.Root(), "switch.lock")
	first, err := acquireFileLock(context.Background(), lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	command := exec.Command(os.Args[0], "-test.run=^TestAccountStoreLockSerializesAcrossProcesses$")
	command.Env = append(os.Environ(),
		"WECLAW_CODEXAUTH_LOCK_HELPER=1",
		"WECLAW_CODEXAUTH_LOCK_PATH="+lockPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock helper error=%v output=%s", err, output)
	}
}

func TestStoreRejectsSymlinkedAccountRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link ownership semantics differ on Windows")
	}
	store := newTestStore(t, newFakeKeyring())
	accountRoot := filepath.Dir(store.Root())
	if err := os.MkdirAll(accountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.Root()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); ErrorCode(err) != CodeInvalid {
		t.Fatalf("symlinked account root error=%v", err)
	}
}

func TestAccountStoreLockRejectsSymlinkAndLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix lock hardening")
	}
	store := newTestStore(t, newFakeKeyring())
	if err := store.ensureStoreRoot(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.Root(), "switch.lock")
	target := filepath.Join(t.TempDir(), "target.lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireFileLock(context.Background(), lockPath); err == nil {
		t.Fatal("符号链接 lock 必须被拒绝")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireFileLock(context.Background(), lockPath); err == nil {
		t.Fatal("权限过宽的 lock 必须被拒绝")
	}
}
