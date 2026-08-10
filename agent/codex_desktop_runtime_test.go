package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestACPAgentDesktopControlledTurnDoesNotStartAppServer(t *testing.T) {
	a, caller := desktopRuntimeTestAgent(t)
	claimDesktopRemoteControl(t, a)
	caller.onCall = func(method string) {
		if method != "thread-follower-start-turn" {
			return
		}
		a.dispatchToTurnCh("thread-1", &codexTurnEvent{TurnID: "turn-1", ItemID: "item-1", Delta: "同一上下文回复"})
		a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "completed", TurnID: "turn-1"})
	}
	caller.result = json.RawMessage(`{"turn":{"id":"turn-1"}}`)

	reply, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: desktopRuntimeRequest(), Message: "继续",
	})
	if err != nil || reply != "同一上下文回复" {
		t.Fatalf("RunCodexTurn() = %q, %v", reply, err)
	}
	if a.isRuntimeStarted() || len(a.threads) != 0 {
		t.Fatalf("app-server started=%v threads=%#v", a.isRuntimeStarted(), a.threads)
	}
}

func TestACPAgentStartPrefersDesktopBridgeWithoutStartingSharedHost(t *testing.T) {
	home := newShortCodexHome(t)
	runtime := newCodexDesktopRuntime()
	runtime.presence = func() (bool, bool) { return true, true }
	hold := make(chan struct{})
	runtime.client = newCodexDesktopClient(codexDesktopTestOptions(codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "desktop-client")
		<-hold
	})))
	t.Cleanup(func() { close(hold) })

	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: codexHostModeAuto,
		CodexDesktopBridge: true, Env: map[string]string{"CODEX_HOME": home},
		StateFile: filepath.Join(home, "state.json"),
	}, acpAgentOptions{desktopProbe: runtime})
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(a.Stop)
	if a.isRuntimeStarted() {
		t.Fatal("Desktop bridge startup must not start or attach a second app-server")
	}
	if got := a.codexRuntimeModeSnapshot(); got != CodexRuntimeDesktop {
		t.Fatalf("runtime mode = %q, want desktop", got)
	}
}

func TestACPAgentExplicitDaemonDoesNotSelectDesktopHost(t *testing.T) {
	runtime := newCodexDesktopRuntime()
	runtime.presence = func() (bool, bool) { return true, true }
	desktopDialed := false
	runtime.client = newCodexDesktopClient(codexDesktopTestOptions(func(context.Context) (net.Conn, error) {
		desktopDialed = true
		return nil, errors.New("explicit daemon must not select Desktop Host")
	}))
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "daemon",
		CodexDesktopBridge: true, Env: map[string]string{"CODEX_HOME": t.TempDir()},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: runtime})
	if !a.codexDesktopCoordination || a.codexDesktopHostSelection {
		t.Fatalf("coordination=%v hostSelection=%v, explicit daemon must coordinate without selecting Desktop Host",
			a.codexDesktopCoordination, a.codexDesktopHostSelection)
	}

	selected, err := a.tryStartCodexDesktopRuntime(context.Background())

	if err != nil || selected || desktopDialed {
		t.Fatalf("selected=%v desktopDialed=%v err=%v", selected, desktopDialed, err)
	}
}

func TestACPAgentLegacyDesktopBridgeStillSelectsDesktopHost(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexDesktopBridge: true,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})

	if !a.codexDesktopCoordination || !a.codexDesktopHostSelection {
		t.Fatalf("coordination=%v hostSelection=%v, legacy explicit bridge must retain Host selection",
			a.codexDesktopCoordination, a.codexDesktopHostSelection)
	}
}

func TestACPAgentStartPrefersRunningOfficialDaemonOverDesktopBridge(t *testing.T) {
	home := newShortCodexHome(t)
	socketPath := codexDaemonSocketPath(home)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := newFakeCodexHost(listener)
	server.start(t)
	t.Cleanup(func() { _ = listener.Close() })

	runtime := newCodexDesktopRuntime()
	runtime.presence = func() (bool, bool) { return true, true }
	desktopDialed := false
	runtime.client = newCodexDesktopClient(codexDesktopTestOptions(func(context.Context) (net.Conn, error) {
		desktopDialed = true
		return nil, errors.New("Desktop IPC must not be selected")
	}))
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "auto",
		Env: map[string]string{"CODEX_HOME": home}, StateFile: filepath.Join(home, "state.json"),
	}, acpAgentOptions{desktopProbe: runtime, desktopBridge: true})
	var lifecycleActions []string
	a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
		lifecycleActions = append(lifecycleActions, action)
		return testCodexDaemonOutput("running", "pid", socketPath), nil
	}
	a.codexDaemonMetadataCall = testCodexDaemonMetadata

	err = a.Start(context.Background())

	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(a.Stop)
	if got := a.codexRuntimeModeSnapshot(); got != CodexRuntimeWeClaw {
		t.Fatalf("runtime mode = %q, want shared daemon", got)
	}
	if desktopDialed {
		t.Fatal("Desktop IPC was dialed while the official daemon was running")
	}
	if len(lifecycleActions) != 2 || lifecycleActions[0] != "version" || lifecycleActions[1] != "version" {
		t.Fatalf("daemon lifecycle actions = %v, want two version validations", lifecycleActions)
	}
}

func TestCodexDesktopRuntimeAnswersFollowingStatusForTrackedAuthoritativeThread(t *testing.T) {
	response := make(chan codexDesktopEnvelope, 1)
	runtime := newCodexDesktopRuntime()
	runtime.setAuthoritative(func() bool { return true })
	runtime.trackThread("thread-1")
	options := codexDesktopTestOptions(codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "weclaw-client")
		writeCodexDesktopTestEnvelope(t, conn, codexDesktopEnvelope{
			Type: codexDesktopEnvelopeBroadcast, SourceClientID: "desktop-client",
			Method: "thread-stream-following-status-requested", Version: 1,
			Params: json.RawMessage(`{"conversationId":"thread-1","hostId":"host-1"}`),
		})
		response <- readCodexDesktopTestEnvelope(t, conn)
	}))
	options.onBroadcast = runtime.handleBroadcast
	client := newCodexDesktopClient(options)
	runtime.mu.Lock()
	runtime.client = client
	runtime.state = newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	runtime.mu.Unlock()
	mustConnectCodexDesktopTestClient(t, client)

	select {
	case envelope := <-response:
		if envelope.Method != "thread-stream-following-changed" || envelope.Version != 1 ||
			len(envelope.TargetClientIDs) != 1 || envelope.TargetClientIDs[0] != "desktop-client" {
			t.Fatalf("response envelope = %#v", envelope)
		}
		var params struct {
			ConversationID string `json:"conversationId"`
			HostID         string `json:"hostId"`
			Following      bool   `json:"following"`
		}
		if err := json.Unmarshal(envelope.Params, &params); err != nil ||
			params.ConversationID != "thread-1" || params.HostID != "host-1" || !params.Following {
			t.Fatalf("response params=%#v err=%v", params, err)
		}
	case <-time.After(codexDesktopTestTimeout):
		t.Fatal("following status response not sent")
	}
}

func TestCodexDesktopFollowingStatusDoesNotClaimUntrackedOrNonAuthoritativeThread(t *testing.T) {
	envelope := codexDesktopEnvelope{
		Type: codexDesktopEnvelopeBroadcast, SourceClientID: "desktop-client",
		Method: "thread-stream-following-status-requested", Version: 1,
		Params: json.RawMessage(`{"conversationId":"thread-1","hostId":"host-1"}`),
	}
	for _, test := range []struct {
		name          string
		tracked       bool
		authoritative bool
	}{
		{name: "untracked", tracked: false, authoritative: true},
		{name: "not authoritative", tracked: true, authoritative: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if status, ok := codexDesktopFollowingStatusResponse(envelope, test.tracked, test.authoritative); ok {
				t.Fatalf("status=%#v, must not claim following", status)
			}
		})
	}
}

func TestACPAgentStartFailsClosedWhenDesktopIsPresentButUnreachable(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	runtime := newCodexDesktopRuntime()
	runtime.presence = func() (bool, bool) { return true, true }
	runtime.client = newCodexDesktopClient(codexDesktopTestOptions(func(context.Context) (net.Conn, error) {
		return nil, errors.New("desktop socket refused")
	}))
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: runtime, desktopBridge: true})

	err := a.Start(context.Background())

	if !errors.Is(err, ErrCodexDesktopOwnershipUnknown) {
		t.Fatalf("Start() error = %v, want ownership unknown", err)
	}
	if a.isRuntimeStarted() {
		t.Fatal("unreachable running Desktop must not fall back to a second app-server")
	}
}

func TestACPAgentStartRejectsUnverifiedExistingSharedHostBeforeDesktopActivation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	runtime := newCodexDesktopRuntime()
	runtime.presence = func() (bool, bool) { return true, true }
	hold := make(chan struct{})
	runtime.client = newCodexDesktopClient(codexDesktopTestOptions(codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "desktop-client")
		<-hold
	})))
	t.Cleanup(func() { close(hold) })
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: runtime, desktopBridge: true})
	sharedSocket, err := a.resolveCodexHostSocket()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sharedSocket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", sharedSocket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	err = a.Start(context.Background())

	if !errors.Is(err, ErrCodexDesktopOwnershipUnknown) {
		t.Fatalf("Start() error = %v, want ownership unknown", err)
	}
	if got := a.codexRuntimeModeSnapshot(); got == CodexRuntimeDesktop {
		t.Fatalf("runtime mode = %q, unverified shared Host must prevent Desktop activation", got)
	}
}

func TestACPAgentStartStopsVerifiedIdleSharedHostBeforeDesktopActivation(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "weclaw-desktop-handoff-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "app-server.sock")
	commandPath := filepath.Join(dir, "codex")
	countPath := filepath.Join(dir, "starts.log")
	script := `#!/bin/sh
socket=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--listen" ]; then
    shift
    socket="${1#unix://}"
  fi
  shift
done
WECLAW_TEST_CODEX_UNIX_HOST_SOCKET="$socket" exec "$WECLAW_TEST_CODEX_BINARY" -test.run='^TestHelperCodexUnixHost$'
`
	if err := os.WriteFile(commandPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := ACPAgentConfig{
		Command: commandPath, Args: []string{"app-server"}, AppServerSocket: socketPath,
		Env: map[string]string{
			"WECLAW_TEST_CODEX_BINARY": os.Args[0], testCodexUnixHostCountEnv: countPath,
		},
	}
	shared := NewACPAgent(config)
	if err := shared.Start(context.Background()); err != nil {
		t.Fatalf("start shared Host: %v", err)
	}
	t.Cleanup(shared.Stop)

	runtime := newCodexDesktopRuntime()
	runtime.presence = func() (bool, bool) { return true, true }
	hold := make(chan struct{})
	runtime.client = newCodexDesktopClient(codexDesktopTestOptions(codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "desktop-client")
		<-hold
	})))
	t.Cleanup(func() { close(hold) })
	config.StateFile = filepath.Join(dir, "desktop-state.json")
	desktop := newACPAgent(config, acpAgentOptions{desktopProbe: runtime, desktopBridge: true})
	if err := desktop.Start(context.Background()); err != nil {
		t.Fatalf("start Desktop bridge: %v", err)
	}
	t.Cleanup(desktop.Stop)

	if got := desktop.codexRuntimeModeSnapshot(); got != CodexRuntimeDesktop {
		t.Fatalf("runtime mode = %q, want desktop", got)
	}
	if desktop.isRuntimeStarted() {
		t.Fatal("shared Host connection must be stopped before Desktop activation")
	}
}

func TestACPAgentDesktopReadStateDoesNotCallThreadRead(t *testing.T) {
	a, caller := desktopRuntimeTestAgent(t)
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		t.Fatal("Desktop state 不应调用 app-server RPC")
		return nil, nil
	}
	state, err := a.ReadCodexThreadState(context.Background(), "conversation-1", "thread-1")
	if err != nil || state.Model != "gpt-test" || len(caller.calls) != 0 {
		t.Fatalf("ReadCodexThreadState() = %#v, %v", state, err)
	}
}

func TestACPAgentDesktopControlsUseFollowerMethods(t *testing.T) {
	a, caller := desktopRuntimeTestAgent(t)
	caller.result = json.RawMessage(`{}`)
	if err := a.SteerCodexThread(context.Background(), "conversation-1", "thread-1", "turn-1", "补充"); err != nil {
		t.Fatalf("SteerCodexThread() error = %v", err)
	}
	if err := a.InterruptCodexThread(context.Background(), "conversation-1", "thread-1", "turn-1"); err != nil {
		t.Fatalf("InterruptCodexThread() error = %v", err)
	}
	if caller.calls[0].method != "thread-follower-steer-turn" || caller.calls[1].method != "thread-follower-interrupt-turn" {
		t.Fatalf("calls = %#v", caller.calls)
	}
}

func TestACPAgentDesktopThreadSettingsUseFollowerMethod(t *testing.T) {
	a, caller := desktopRuntimeTestAgent(t)
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		t.Fatal("Desktop thread settings must not call app-server RPC")
		return nil, nil
	}
	serviceTier := CodexServiceTierStandard
	if err := a.SetCodexThreadConfig(context.Background(), CodexThreadConfigUpdate{
		ThreadID: "thread-1", Model: "gpt-next", Effort: "max", ServiceTier: &serviceTier,
	}); err != nil {
		t.Fatalf("SetCodexThreadConfig() error = %v", err)
	}
	if len(caller.calls) != 1 || caller.calls[0].method != "thread-follower-update-thread-settings" {
		t.Fatalf("calls = %#v", caller.calls)
	}
}

func TestACPAgentDesktopResetSessionFailsWithoutDeletingBinding(t *testing.T) {
	a, _ := desktopRuntimeTestAgent(t)
	a.setCodexRuntimeMode(CodexRuntimeDesktop)
	a.mu.Lock()
	a.threads["conversation-1"] = "thread-1"
	a.mu.Unlock()
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		t.Fatal("Desktop /new must not call app-server RPC")
		return nil, nil
	}

	_, err := a.ResetSession(context.Background(), "conversation-1")

	if !errors.Is(err, ErrCodexDesktopCapabilityUnavailable) {
		t.Fatalf("ResetSession() error = %v", err)
	}
	a.mu.Lock()
	threadID := a.threads["conversation-1"]
	a.mu.Unlock()
	if threadID != "thread-1" {
		t.Fatalf("binding = %q, must remain unchanged", threadID)
	}
}

func TestACPAgentDisconnectedControlsReturnTypedError(t *testing.T) {
	a, _ := desktopRuntimeTestAgent(t)
	a.codexOwners.markDesktopDisconnected()
	err := a.InterruptCodexThread(context.Background(), "conversation-1", "thread-1", "turn-1")
	if !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("InterruptCodexThread() error = %v", err)
	}
}

func TestACPAgentDesktopControlledTurnDoesNotAutoRecover(t *testing.T) {
	a, caller := desktopRuntimeTestAgent(t)
	claimDesktopRemoteControl(t, a)
	caller.err = ErrCodexDesktopNoClient
	restarts := 0
	a.restartCodexAppServerCall = func(context.Context) error { restarts++; return nil }
	_, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: desktopRuntimeRequest(), Message: "继续",
	})
	if !errors.Is(err, ErrCodexDesktopNoClient) || restarts != 0 {
		t.Fatalf("error=%v restarts=%d", err, restarts)
	}
}

func TestACPAgentDesktopDisconnectInvalidatesRuntimeWithoutReleasingRemoteOwner(t *testing.T) {
	desktopRuntime := newCodexDesktopRuntime()
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: desktopRuntime})
	disconnect := make(chan struct{})
	client := a.desktopRuntime.ensureInitialized()
	client.dial = codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		<-disconnect
	})
	mustConnectCodexDesktopTestClient(t, client)

	req := desktopRuntimeRequest()
	state := CodexThreadState{ThreadID: req.Ref.ThreadID, Model: "gpt-test"}
	a.codexOwners.observeDesktopSnapshot(req.Ref.ThreadID, 1, state)
	if _, err := a.codexOwners.activateRuntime(req, CodexRuntimeDesktop, state); err != nil {
		t.Fatal(err)
	}

	close(disconnect)
	waitCodexDesktopDisconnected(t, client)
	deadline := time.Now().Add(codexDesktopTestTimeout)
	for time.Now().Before(deadline) {
		binding, err := a.CurrentCodexRuntime(req)
		if err != nil {
			t.Fatal(err)
		}
		if binding.Runtime == CodexRuntimeUnknown {
			if binding.Control != req.Intent {
				t.Fatalf("control = %#v, want %#v", binding.Control, req.Intent)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	binding, err := a.CurrentCodexRuntime(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("runtime = %s, want %s; control = %#v", binding.Runtime, CodexRuntimeUnknown, binding.Control)
}

// TestACPAgentDesktopWatchReconcilesCompletedState 验证终态事件缺失时仍能从权威状态收尾。
func TestACPAgentDesktopWatchReconcilesCompletedState(t *testing.T) {
	a, _ := desktopRuntimeTestAgent(t)
	applyDesktopRuntimeTestState(t, a, 2, "inProgress", "")
	reconcile := make(chan time.Time, 1)
	result := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		text, err := a.watchCodexThreadWithReconcile(context.Background(), codexThreadWatchOptions{
			conversationID: "conversation-1", threadID: "thread-1", reconcile: reconcile,
		})
		result <- text
		errCh <- err
	}()
	waitForDesktopTurnWatcher(t, a, "thread-1")
	applyDesktopRuntimeTestState(t, a, 3, "completed", "状态复核后的结果")
	reconcile <- time.Now()
	if err := <-errCh; err != nil {
		t.Fatalf("watchCodexThreadWithReconcile() error = %v", err)
	}
	if text := <-result; text != "状态复核后的结果" {
		t.Fatalf("result = %q", text)
	}
}

// applyDesktopRuntimeTestState 更新测试 runtime，但故意不投递 turn event。
func applyDesktopRuntimeTestState(t *testing.T, a *ACPAgent, revision uint64, status string, text string) {
	t.Helper()
	raw := desktopStateFixture("thread-1", "active")
	items := []any{}
	if text != "" {
		items = append(items, map[string]any{
			"id": "agent-1", "type": "agentMessage", "status": "completed", "text": text,
		})
	}
	raw["turns"] = []any{desktopTurnFixture("turn-1", status, items)}
	if status != "inProgress" {
		raw["threadRuntimeStatus"] = map[string]any{"type": "idle"}
	}
	if _, err := a.desktopRuntime.state.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: revision, raw: raw,
	}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	snapshot, found := a.desktopRuntime.state.snapshot("thread-1")
	if !found {
		t.Fatal("Desktop state snapshot 不存在")
	}
	a.codexOwners.observeDesktopSnapshot("thread-1", revision, snapshot.State)
}

// waitForDesktopTurnWatcher 等待观察通道完成注册。
func waitForDesktopTurnWatcher(t *testing.T, a *ACPAgent, threadID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.notifyMu.Lock()
		registered := a.turnCh[threadID] != nil
		a.notifyMu.Unlock()
		if registered {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Desktop turn watcher 未注册")
}

func desktopRuntimeTestAgent(t *testing.T) (*ACPAgent, *codexDesktopActionCaller) {
	t.Helper()
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	caller := &codexDesktopActionCaller{}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	state := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now, actions: actions})
	raw := desktopStateFixture("thread-1", "idle")
	if _, err := state.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	a.desktopRuntime = &codexDesktopRuntime{state: state, actions: actions}
	threadState := CodexThreadState{
		ThreadID: "thread-1", Model: "gpt-test",
	}
	a.codexOwners.observeDesktopSnapshot("thread-1", 1, threadState)
	binding, _ := a.codexOwners.threadBinding("thread-1")
	a.codexOwners.bindConversation(desktopRuntimeRequest().Ref, binding)
	return a, caller
}

func claimDesktopRemoteControl(t *testing.T, a *ACPAgent) {
	t.Helper()
	binding, _ := a.codexOwners.threadBinding("thread-1")
	if _, err := a.codexOwners.activateRuntime(desktopRuntimeRequest(), CodexRuntimeDesktop, binding.State); err != nil {
		t.Fatal(err)
	}
}

func desktopRuntimeRequest() CodexRuntimeRequest {
	return CodexRuntimeRequest{
		Ref: CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
		Intent: CodexControlIntent{
			Owner: CodexControlRemote, RouteKey: "route-1",
			ConversationID: "conversation-1", Revision: 1,
		},
	}
}
