package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestACPAgentDesktopChatDoesNotStartAppServer(t *testing.T) {
	a, caller := desktopRuntimeTestAgent(t)
	caller.onCall = func(method string) {
		if method != "thread-follower-start-turn" {
			return
		}
		a.dispatchToTurnCh("thread-1", &codexTurnEvent{TurnID: "turn-1", ItemID: "item-1", Delta: "同一上下文回复"})
		a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "completed", TurnID: "turn-1"})
	}
	caller.result = json.RawMessage(`{"turn":{"id":"turn-1"}}`)

	reply, err := a.Chat(context.Background(), "conversation-1", "继续")
	if err != nil || reply != "同一上下文回复" {
		t.Fatalf("Chat() = %q, %v", reply, err)
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
	if !errors.Is(err, ErrCodexDesktopDisconnected) {
		t.Fatalf("InterruptCodexThread() error = %v", err)
	}
}

// TestACPAgentDesktopChatRecoversAfterNoClient 验证 Desktop 明确释放后原消息会在同一 thread 单次续跑。
func TestACPAgentDesktopChatRecoversAfterNoClient(t *testing.T) {
	a, caller := desktopRuntimeTestAgent(t)
	caller.err = ErrCodexDesktopNoClient
	restarts := 0
	a.restartCodexAppServerCall = func(context.Context) error {
		restarts++
		return nil
	}
	var methods []string
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "thread/resume":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			turn := params.(codexTurnStartParams)
			if len(turn.Input) != 1 || turn.Input[0].Text != "继续" {
				return nil, fmt.Errorf("unexpected retry input: %#v", turn.Input)
			}
			a.dispatchToTurnCh(turn.ThreadID, &codexTurnEvent{ItemID: "item-1", Delta: "恢复后的回复"})
			a.dispatchToTurnCh(turn.ThreadID, &codexTurnEvent{Kind: "completed"})
			return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	reply, err := a.Chat(context.Background(), "conversation-1", "继续")
	if err != nil || reply != "恢复后的回复" {
		t.Fatalf("Chat() = %q, %v", reply, err)
	}
	if restarts != 1 || len(caller.calls) != 1 || len(methods) != 2 || methods[0] != "thread/resume" || methods[1] != "turn/start" {
		t.Fatalf("restarts=%d desktopCalls=%d methods=%v", restarts, len(caller.calls), methods)
	}
	binding, ok := a.CurrentCodexThreadBinding("conversation-1")
	if !ok || binding.Owner != CodexOwnerWeClawRuntime || binding.Ref.ThreadID != "thread-1" {
		t.Fatalf("binding=%#v ok=%v", binding, ok)
	}
}

// TestACPAgentDesktopChatDoesNotRecoverAmbiguousErrors 验证非确定性错误不会转移所有权或重试消息。
func TestACPAgentDesktopChatDoesNotRecoverAmbiguousErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "连接断开", err: ErrCodexDesktopDisconnected},
		{name: "交付状态未知", err: ErrCodexDesktopDeliveryUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a, caller := desktopRuntimeTestAgent(t)
			caller.err = test.err
			restarts := 0
			a.restartCodexAppServerCall = func(context.Context) error { restarts++; return nil }

			_, err := a.Chat(context.Background(), "conversation-1", "继续")
			if !errors.Is(err, test.err) || restarts != 0 {
				t.Fatalf("Chat() error=%v restarts=%d", err, restarts)
			}
			binding, ok := a.CurrentCodexThreadBinding("conversation-1")
			if !ok || binding.Owner != CodexOwnerDesktopLive || binding.ReleaseConfirmed {
				t.Fatalf("binding=%#v ok=%v", binding, ok)
			}
		})
	}
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
	a.codexOwners.observeDesktopSnapshot("thread-1", 1, CodexThreadState{
		ThreadID: "thread-1", Model: "gpt-test",
	})
	a.codexOwners.bindConversation(CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	}, CodexThreadBinding{Owner: CodexOwnerDesktopLive, Connected: true})
	return a, caller
}
