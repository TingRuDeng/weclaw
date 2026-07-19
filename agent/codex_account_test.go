package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

type accountTestKeyring struct {
	mu     sync.Mutex
	values map[string]string
}

func (f *accountTestKeyring) Get(service, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.values[service+"\x00"+user]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}

func (f *accountTestKeyring) Set(service, user, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[service+"\x00"+user] = password
	return nil
}

func (f *accountTestKeyring) Delete(service, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, service+"\x00"+user)
	return nil
}

func accountTestAuth(t *testing.T, email, accountID string) *codexauth.Snapshot {
	t.Helper()
	claims, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		t.Fatal(err)
	}
	idToken := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
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

type accountSwitchFixture struct {
	agent           *ACPAgent
	store           *codexauth.Store
	oldProfile      codexauth.Profile
	target          codexauth.Profile
	oldSnapshot     *codexauth.Snapshot
	newSnapshot     *codexauth.Snapshot
	runtimeMail     string
	threadState     string
	threadListCalls int
	threadListHook  func(int, interface{}) (json.RawMessage, error)
	stopCalls       int
	startCalls      int
	startHook       func(*accountSwitchFixture) error
}

func newAccountSwitchFixture(t *testing.T) *accountSwitchFixture {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "weclaw")
	codexHome := filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(t.TempDir(), "codex.sock")
	keyring := &accountTestKeyring{values: make(map[string]string)}
	store, err := codexauth.NewStore(codexauth.StoreOptions{
		DataDir: dataDir, CodexHome: codexHome, SocketPath: socketPath, Keyring: keyring,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldSnapshot := accountTestAuth(t, "old@example.com", "old-account")
	newSnapshot := accountTestAuth(t, "new@example.com", "new-account")
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
	a := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"},
		AppServerSocket: socketPath, StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	fixture := &accountSwitchFixture{
		agent: a, store: store, oldProfile: oldProfile, target: target,
		oldSnapshot: oldSnapshot, newSnapshot: newSnapshot, runtimeMail: "old@example.com", threadState: "idle",
	}
	a.codexAccountStoreCall = func() (*codexauth.Store, error) { return store, nil }
	a.updateHostIdentityCall = func(string, codexauth.Profile) error { return nil }
	a.stopManagedHostCall = func(context.Context, string) error {
		fixture.stopCalls++
		a.mu.Lock()
		a.started = false
		a.mu.Unlock()
		return nil
	}
	a.startManagedHostCall = func(context.Context, string) error {
		fixture.startCalls++
		if fixture.startHook != nil {
			if err := fixture.startHook(fixture); err != nil {
				return err
			}
		}
		snapshot, err := codexauth.ReadAuthFile(store.AuthPath())
		if err != nil {
			return err
		}
		switch {
		case snapshot.MatchesEmail("new@example.com"):
			fixture.runtimeMail = "new@example.com"
		case snapshot.MatchesEmail("old@example.com"):
			fixture.runtimeMail = "old@example.com"
		default:
			fixture.runtimeMail = "unknown@example.com"
		}
		a.mu.Lock()
		a.started = true
		a.mu.Unlock()
		return nil
	}
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "account/read":
			return json.RawMessage(`{"account":{"type":"chatgpt","email":"` + fixture.runtimeMail + `","planType":"plus"},"requiresOpenaiAuth":false}`), nil
		case "account/rateLimits/read":
			return json.RawMessage(`{"rateLimitsByLimitId":{}}`), nil
		case "thread/list":
			fixture.threadListCalls++
			if fixture.threadListHook != nil {
				return fixture.threadListHook(fixture.threadListCalls, params)
			}
			return json.RawMessage(`{"data":[{"id":"thread-1","status":{"type":"` + fixture.threadState + `"}}],"nextCursor":null}`), nil
		default:
			return nil, errors.New("unexpected rpc: " + method)
		}
	}
	a.mu.Lock()
	a.started = true
	a.threads["conversation-1"] = "thread-1"
	a.mu.Unlock()
	return fixture
}

func markAccountFixtureHostManaged(t *testing.T, fixture *accountSwitchFixture) codexHostMetadata {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	configureACPProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	identity, err := inspectCodexHostProcess(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, State: "running", PID: cmd.Process.Pid,
		ProcessGroupID: identity.pgid, UID: identity.uid, ProcessStart: identity.start,
		ObservedCommandHash: identity.commandHash,
		CommandFingerprint:  fixture.agent.configuredCodexHostCommandFingerprint(fixture.store.SocketPath()),
		SocketPath:          fixture.store.SocketPath(), Generation: 3, StartedAt: time.Now().UTC(),
		ActiveProfileID: string(fixture.oldProfile.ID), AccountFingerprint: fixture.oldProfile.AccountFingerprint,
	}
	if err := fixture.agent.writeCodexHostMetadata(fixture.store.SocketPath(), metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func TestReadCodexAccountAcceptsLoggedInChatGPTAccountWhenProviderRequiresAuth(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "account/read" {
			t.Fatalf("method=%q, want account/read", method)
		}
		refresh, ok := params.(map[string]bool)
		if !ok || !refresh["refreshToken"] {
			t.Fatalf("params=%#v, want refreshToken=true", params)
		}
		return json.RawMessage(`{"account":{"type":"chatgpt","email":"user@example.com","planType":"pro"},"requiresOpenaiAuth":true}`), nil
	}

	account, err := a.readCodexAccount(context.Background(), true)
	if err != nil {
		t.Fatalf("readCodexAccount() error=%v", err)
	}
	if account.Email != "user@example.com" || account.PlanType != "pro" {
		t.Fatalf("readCodexAccount()=%+v", account)
	}
}

func TestReadCodexAccountRejectsMissingAccount(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		return json.RawMessage(`{"account":null,"requiresOpenaiAuth":true}`), nil
	}

	_, err := a.readCodexAccount(context.Background(), false)
	if codexauth.ErrorCode(err) != codexauth.CodeRuntimeUnavailable {
		t.Fatalf("readCodexAccount() error=%v, code=%q", err, codexauth.ErrorCode(err))
	}
}

func TestUseCodexAccountRestartsHostAndPreservesThreadBindings(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	var phases []CodexAccountSwitchPhase
	ctx := WithCodexAccountSwitchProgress(context.Background(), func(phase CodexAccountSwitchPhase) {
		phases = append(phases, phase)
	})
	before, err := fixture.store.Status()
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.agent.UseCodexAccount(ctx, fixture.target.Label, before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Current.ID != fixture.target.ID || fixture.stopCalls != 1 || fixture.startCalls != 1 {
		t.Fatalf("result=%#v stop=%d start=%d", result, fixture.stopCalls, fixture.startCalls)
	}
	current, _, err := fixture.store.Current()
	if err != nil || current == nil || current.ID != fixture.target.ID {
		t.Fatalf("current=%#v error=%v", current, err)
	}
	auth, err := codexauth.ReadAuthFile(fixture.store.AuthPath())
	if err != nil || !auth.MatchesEmail("new@example.com") {
		t.Fatalf("target auth not projected: error=%v", err)
	}
	fixture.agent.mu.Lock()
	thread := fixture.agent.threads["conversation-1"]
	resume := fixture.agent.resumeOnFirstUse["conversation-1"]
	fixture.agent.mu.Unlock()
	if thread != "thread-1" || !resume {
		t.Fatalf("thread binding changed: thread=%q resume=%v", thread, resume)
	}
	if fixture.agent.ensureCodexAppServerGate().generation() != 2 {
		t.Fatalf("gate generation=%d", fixture.agent.ensureCodexAppServerGate().generation())
	}
	wantPhases := []CodexAccountSwitchPhase{CodexAccountSwitchChecking, CodexAccountSwitchSwitching, CodexAccountSwitchVerifying}
	if !reflect.DeepEqual(phases, wantPhases) {
		t.Fatalf("phases=%v want=%v", phases, wantPhases)
	}
}

func TestSaveCodexAccountRejectsUnmanagedHostWithoutStoreMutation(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	before, err := fixture.store.Status()
	if err != nil {
		t.Fatal(err)
	}

	_, err = fixture.agent.SaveCodexAccount(context.Background(), CodexAccountSaveOptions{Label: "不应保存"})
	if codexauth.ErrorCode(err) != codexauth.CodeUnmanagedHost {
		t.Fatalf("SaveCodexAccount() error=%v", err)
	}

	after, err := fixture.store.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("unmanaged host changed account store:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestRemoveCodexAccountUsesRuntimeGate(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	permit, err := fixture.agent.ensureCodexAppServerGate().acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer permit.release()
	before, err := fixture.store.Status()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.agent.RemoveCodexAccount(context.Background(), fixture.target.Label); codexauth.ErrorCode(err) != codexauth.CodeBusy {
		t.Fatalf("RemoveCodexAccount() error=%v", err)
	}
	after, err := fixture.store.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("busy remove changed store:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestDoctorCodexAccountsPreservesUnsafeStoreResult(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	if err := fixture.store.WithTransaction(context.Background(), func(tx *codexauth.Transaction) error {
		tx.SetLastSwitch(codexauth.SwitchRecord{
			ProfileID: fixture.oldProfile.ID, Status: "rollback_failed", Message: "rollback failed", At: time.Now(),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result := fixture.agent.DoctorCodexAccounts(context.Background())
	if result.OK || !strings.Contains(result.Message, "禁止写入") {
		t.Fatalf("DoctorCodexAccounts()=%#v", result)
	}
}

func TestSaveCodexAccountRollsBackStoreAndHostMetadataTogether(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	beforeMetadata := markAccountFixtureHostManaged(t, fixture)
	before, err := fixture.store.Status()
	if err != nil {
		t.Fatal(err)
	}
	fixture.agent.updateHostIdentityCall = func(socketPath string, profile codexauth.Profile) error {
		partial := beforeMetadata
		partial.ActiveProfileID = string(profile.ID)
		partial.AccountFingerprint = profile.AccountFingerprint
		if err := fixture.agent.writeCodexHostMetadata(socketPath, partial); err != nil {
			return err
		}
		return errors.New("metadata fsync outcome unknown")
	}

	_, err = fixture.agent.SaveCodexAccount(context.Background(), CodexAccountSaveOptions{Label: "待补偿账号"})
	if err == nil {
		t.Fatal("SaveCodexAccount() unexpectedly succeeded")
	}
	after, statusErr := fixture.store.Status()
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed save changed account store:\nbefore=%#v\nafter=%#v", before, after)
	}
	afterMetadata, metadataErr := fixture.agent.readCodexHostMetadata(fixture.store.SocketPath())
	if metadataErr != nil {
		t.Fatal(metadataErr)
	}
	if !reflect.DeepEqual(afterMetadata, beforeMetadata) {
		t.Fatalf("failed save changed host metadata:\nbefore=%#v\nafter=%#v", beforeMetadata, afterMetadata)
	}
}

func TestUseCodexAccountRollsBackTargetMismatch(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	fixture.startHook = func(f *accountSwitchFixture) error {
		if f.startCalls == 1 {
			f.runtimeMail = "wrong@example.com"
			f.agent.mu.Lock()
			f.agent.started = true
			f.agent.mu.Unlock()
			return errors.New("keep mismatched runtime identity")
		}
		return nil
	}
	// 第一次目标启动需要成功但返回错误账号，因此单独覆盖启动逻辑。
	fixture.agent.startManagedHostCall = func(context.Context, string) error {
		fixture.startCalls++
		if fixture.startCalls == 1 {
			fixture.runtimeMail = "wrong@example.com"
		} else {
			fixture.runtimeMail = "old@example.com"
		}
		fixture.agent.mu.Lock()
		fixture.agent.started = true
		fixture.agent.mu.Unlock()
		return nil
	}
	before, _ := fixture.store.Status()
	_, err := fixture.agent.UseCodexAccount(context.Background(), fixture.target.Label, before.Revision)
	if codexauth.ErrorCode(err) != codexauth.CodeTargetMismatch {
		t.Fatalf("UseCodexAccount() error=%v", err)
	}
	if fixture.stopCalls != 2 || fixture.startCalls != 2 {
		t.Fatalf("rollback stop=%d start=%d", fixture.stopCalls, fixture.startCalls)
	}
	current, _, currentErr := fixture.store.Current()
	if currentErr != nil || current == nil || current.ID != fixture.oldProfile.ID {
		t.Fatalf("current=%#v error=%v", current, currentErr)
	}
	auth, authErr := codexauth.ReadAuthFile(fixture.store.AuthPath())
	if authErr != nil || !auth.MatchesEmail("old@example.com") {
		t.Fatalf("old auth not restored: %v", authErr)
	}
	if fixture.agent.ensureCodexAppServerGate().stateSnapshot() != codexAppServerRunning {
		t.Fatalf("gate state=%s", fixture.agent.ensureCodexAppServerGate().stateSnapshot())
	}
}

func TestUseCodexAccountRollbackFailureFailsClosed(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	fixture.agent.startManagedHostCall = func(context.Context, string) error {
		fixture.startCalls++
		fixture.agent.mu.Lock()
		fixture.agent.started = false
		fixture.agent.mu.Unlock()
		return errors.New("host start failed")
	}
	before, _ := fixture.store.Status()
	_, err := fixture.agent.UseCodexAccount(context.Background(), fixture.target.Label, before.Revision)
	if codexauth.ErrorCode(err) != codexauth.CodeRollbackFailed {
		t.Fatalf("UseCodexAccount() error=%v", err)
	}
	if fixture.agent.ensureCodexAppServerGate().stateSnapshot() != codexAppServerFailed {
		t.Fatalf("gate state=%s", fixture.agent.ensureCodexAppServerGate().stateSnapshot())
	}
	if _, acquireErr := fixture.agent.ensureCodexAppServerGate().acquire(context.Background()); !errors.Is(acquireErr, ErrCodexRuntimeUnavailable) {
		t.Fatalf("failed gate acquire error=%v", acquireErr)
	}

	// fail-closed 不能只存在于当前进程内存；服务重启后仍必须从账户索引恢复。
	restarted := NewACPAgent(ACPAgentConfig{
		ConfiguredName:  "codex",
		Command:         "codex",
		Args:            []string{"app-server"},
		AppServerSocket: fixture.store.SocketPath(),
	})
	restarted.codexAccountStoreCall = func() (*codexauth.Store, error) { return fixture.store, nil }
	if restarted.ensureCodexAppServerGate().stateSnapshot() != codexAppServerFailed {
		t.Fatalf("restarted gate state=%s", restarted.ensureCodexAppServerGate().stateSnapshot())
	}
	if _, acquireErr := restarted.ensureCodexAppServerGate().acquire(context.Background()); !errors.Is(acquireErr, ErrCodexRuntimeUnavailable) {
		t.Fatalf("restarted failed gate acquire error=%v", acquireErr)
	}
}

func TestUseCodexAccountPersistsSwitchJournalBeforeStoppingHost(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	fixture.agent.stopManagedHostCall = func(context.Context, string) error {
		status, err := fixture.store.Status()
		if err != nil {
			t.Fatal(err)
		}
		if status.LastSwitch == nil || status.LastSwitch.Status != "switching" || status.LastSwitch.ProfileID != fixture.target.ID {
			t.Fatalf("switch journal=%#v", status.LastSwitch)
		}
		return errors.New("host stop outcome unknown")
	}
	before, _ := fixture.store.Status()
	_, err := fixture.agent.UseCodexAccount(context.Background(), fixture.target.Label, before.Revision)
	if err == nil {
		t.Fatal("UseCodexAccount() unexpectedly succeeded")
	}
	if fixture.agent.ensureCodexAppServerGate().stateSnapshot() != codexAppServerFailed {
		t.Fatalf("gate state=%s", fixture.agent.ensureCodexAppServerGate().stateSnapshot())
	}

	restarted := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"},
		AppServerSocket: fixture.store.SocketPath(), StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	restarted.codexAccountStoreCall = func() (*codexauth.Store, error) { return fixture.store, nil }
	if restarted.ensureCodexAppServerGate().stateSnapshot() != codexAppServerFailed {
		t.Fatalf("restarted gate state=%s", restarted.ensureCodexAppServerGate().stateSnapshot())
	}
}

func TestUseCodexAccountRejectsActiveOrUncertainWorkWithoutMutation(t *testing.T) {
	t.Run("active app-server thread", func(t *testing.T) {
		fixture := newAccountSwitchFixture(t)
		fixture.threadState = "active"
		before, _ := fixture.store.Status()
		_, err := fixture.agent.UseCodexAccount(context.Background(), fixture.target.Label, before.Revision)
		if codexauth.ErrorCode(err) != codexauth.CodeBusy || fixture.stopCalls != 0 || fixture.startCalls != 0 {
			t.Fatalf("error=%v stop=%d start=%d", err, fixture.stopCalls, fixture.startCalls)
		}
		auth, _ := codexauth.ReadAuthFile(fixture.store.AuthPath())
		if !auth.MatchesEmail("old@example.com") {
			t.Fatal("active-thread rejection changed auth")
		}
	})

	t.Run("uncertain writer lease", func(t *testing.T) {
		fixture := newAccountSwitchFixture(t)
		fixture.agent.codexOwners.mu.Lock()
		fixture.agent.codexOwners.leases["thread-1"] = &codexWriterLeaseState{uncertain: true}
		fixture.agent.codexOwners.mu.Unlock()
		before, _ := fixture.store.Status()
		_, err := fixture.agent.UseCodexAccount(context.Background(), fixture.target.Label, before.Revision)
		if codexauth.ErrorCode(err) != codexauth.CodeBusy || fixture.stopCalls != 0 {
			t.Fatalf("error=%v stop=%d", err, fixture.stopCalls)
		}
	})

	t.Run("unknown app-server thread", func(t *testing.T) {
		fixture := newAccountSwitchFixture(t)
		fixture.threadState = "mystery"
		before, _ := fixture.store.Status()
		_, err := fixture.agent.UseCodexAccount(context.Background(), fixture.target.Label, before.Revision)
		if codexauth.ErrorCode(err) != codexauth.CodeBusy || fixture.stopCalls != 0 {
			t.Fatalf("error=%v stop=%d", err, fixture.stopCalls)
		}
	})
}

func TestUseCodexAccountRechecksAllThreadsImmediatelyBeforeStoppingHost(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	fixture.threadListHook = func(call int, _ interface{}) (json.RawMessage, error) {
		state := "idle"
		// 第一次 ensureAllCodexThreadsIdle 会查询未归档和已归档两页；
		// lifecycle lock 内的第二次检查从第 3 次调用开始。
		if call == 3 {
			state = "active"
		}
		return json.RawMessage(`{"data":[{"id":"thread-1","status":{"type":"` + state + `"}}],"nextCursor":null}`), nil
	}
	before, _ := fixture.store.Status()
	_, err := fixture.agent.UseCodexAccount(context.Background(), fixture.target.Label, before.Revision)
	if codexauth.ErrorCode(err) != codexauth.CodeBusy || fixture.stopCalls != 0 || fixture.startCalls != 0 {
		t.Fatalf("error=%v calls=%d stop=%d start=%d", err, fixture.threadListCalls, fixture.stopCalls, fixture.startCalls)
	}
	auth, authErr := codexauth.ReadAuthFile(fixture.store.AuthPath())
	if authErr != nil || !auth.MatchesEmail("old@example.com") {
		t.Fatalf("late active thread changed auth: %v", authErr)
	}
}

func TestEnsureAllCodexThreadsIdleTraversesPagination(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	fixture.threadListHook = func(call int, params interface{}) (json.RawMessage, error) {
		request, _ := params.(map[string]interface{})
		if call == 1 {
			if _, hasCursor := request["cursor"]; hasCursor {
				t.Fatalf("first page unexpectedly had cursor: %#v", request)
			}
			return json.RawMessage(`{"data":[{"id":"thread-1","status":{"type":"idle"}}],"nextCursor":"page-2"}`), nil
		}
		if call == 2 {
			if request["cursor"] != "page-2" {
				t.Fatalf("second page cursor=%#v", request["cursor"])
			}
			return json.RawMessage(`{"data":[{"id":"thread-2","status":{"type":"active"}}],"nextCursor":null}`), nil
		}
		return nil, errors.New("unexpected page")
	}
	if err := fixture.agent.ensureAllCodexThreadsIdle(context.Background()); codexauth.ErrorCode(err) != codexauth.CodeBusy {
		t.Fatalf("error=%v", err)
	}
	if fixture.threadListCalls != 2 {
		t.Fatalf("thread/list calls=%d", fixture.threadListCalls)
	}
}

func TestConcurrentCodexAccountSwitchCommitsOnlyOnce(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.threadListHook = func(call int, _ interface{}) (json.RawMessage, error) {
		if call == 1 {
			close(entered)
			<-release
		}
		return json.RawMessage(`{"data":[{"id":"thread-1","status":{"type":"idle"}}],"nextCursor":null}`), nil
	}
	before, _ := fixture.store.Status()
	firstDone := make(chan error, 1)
	go func() {
		_, err := fixture.agent.UseCodexAccount(context.Background(), fixture.target.Label, before.Revision)
		firstDone <- err
	}()
	<-entered
	if _, err := fixture.agent.UseCodexAccount(context.Background(), fixture.target.Label, before.Revision); codexauth.ErrorCode(err) != codexauth.CodeBusy {
		t.Fatalf("second switch error=%v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first switch error=%v", err)
	}
	if fixture.stopCalls != 1 || fixture.startCalls != 1 {
		t.Fatalf("stop=%d start=%d", fixture.stopCalls, fixture.startCalls)
	}
}
