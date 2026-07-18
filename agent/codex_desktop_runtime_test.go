package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
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
