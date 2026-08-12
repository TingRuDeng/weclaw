package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCodexDesktopPendingApprovalSurvivesDisconnectAndIsNotReplayed(t *testing.T) {
	caller := &codexDesktopActionCaller{err: ErrCodexDesktopDisconnected}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now, actions: actions})
	raw := desktopStateFixture("thread-1", "idle")
	raw["requests"] = []any{desktopPendingRequestFixture(
		"request-1", "item/commandExecution/requestApproval",
	)}

	first, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot(first) error = %v", err)
	}
	approval := findCodexDesktopApprovalEvent(t, first.Events)
	if err := approval.Approval.Respond(context.Background(), "accept"); err == nil {
		t.Fatal("Respond() error = nil")
	}
	if _, ok := first.Snapshot.Requests["request-1"]; !ok {
		t.Fatal("pending approval was removed after disconnect")
	}

	second, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 2, raw: raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot(second) error = %v", err)
	}
	retry := findCodexDesktopApprovalEvent(t, second.Events)
	caller.err = nil
	caller.result = json.RawMessage(`{}`)
	if err := retry.Approval.Respond(context.Background(), "accept"); err != nil {
		t.Fatalf("retry Respond() error = %v", err)
	}

	resolved := desktopStateFixture("thread-1", "idle")
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 3, raw: resolved,
	}); err != nil {
		t.Fatalf("applySnapshot(resolved) error = %v", err)
	}
	stale, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 4, raw: raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot(stale) error = %v", err)
	}
	if findCodexDesktopActionEvent(stale.Events) != nil {
		t.Fatalf("resolved approval replayed = %#v", stale.Events)
	}
}

func TestCodexDesktopProjectorEmitsUserInputOnce(t *testing.T) {
	caller := &codexDesktopActionCaller{result: json.RawMessage(`{}`)}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now, actions: actions})
	raw := desktopStateFixture("thread-1", "idle")
	raw["requests"] = []any{desktopPendingRequestFixture("request-1", "item/tool/requestUserInput")}
	request := raw["requests"].([]any)[0].(map[string]any)
	request["params"].(map[string]any)["questions"] = desktopUserInputFixture().Params["questions"]

	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	event := findCodexDesktopActionEvent(update.Events)
	if event == nil || event.UserInput == nil {
		t.Fatalf("events = %#v", update.Events)
	}
}

func TestCodexDesktopPendingApprovalReplaysAfterMissingConsumer(t *testing.T) {
	caller := &codexDesktopActionCaller{}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now, actions: actions})
	raw := desktopStateFixture("thread-1", "active")
	raw["turns"] = []any{desktopTurnFixture("turn-1", "inProgress", nil)}
	raw["requests"] = []any{desktopPendingRequestFixture(
		"request-1", "item/commandExecution/requestApproval",
	)}
	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	original := findCodexDesktopApprovalEvent(t, update.Events)
	runtime := &codexDesktopRuntime{state: store, actions: actions}
	runtime.abandonTurnEvent("thread-1", original)

	replayed := findCodexDesktopActionEvent(runtime.replayActiveTurnEvents("thread-1"))
	if replayed == nil || replayed.Approval == nil || replayed.Approval.Request.RequestID != "request-1" {
		t.Fatalf("pending approval was not replayed: %#v", replayed)
	}
	if duplicate := findCodexDesktopActionEvent(runtime.replayActiveTurnEvents("thread-1")); duplicate != nil {
		t.Fatalf("pending approval replayed twice without abandon: %#v", duplicate)
	}
}

func TestCodexDesktopPendingApprovalIsAbandonedBeforeFollowerWake(t *testing.T) {
	caller := &codexDesktopActionCaller{}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	state := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now, actions: actions})
	runtime := &codexDesktopRuntime{state: state, actions: actions}
	raw := desktopStateFixture("thread-1", "active")
	raw["turns"] = []any{desktopTurnFixture("turn-1", "inProgress", nil)}
	raw["requests"] = []any{map[string]any{
		"id":     "request-1",
		"method": "item/commandExecution/requestApproval",
		"params": map[string]any{
			"command": []any{"git", "status"},
			"availableDecisions": []any{
				map[string]any{"decision": "accept"},
				map[string]any{"decision": "decline"},
			},
		},
	}}
	update, err := runtime.state.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 2, raw: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	approval := findCodexDesktopApprovalEvent(t, update.Events)

	a := newACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}}, acpAgentOptions{})
	a.desktopRuntime = runtime
	replayed := make(chan int, 1)
	a.SetCodexThreadActivityHandler(func(threadID string) {
		replayed <- len(runtime.replayActiveTurnEvents(threadID))
	})
	if a.dispatchDesktopTurnEvent("thread-1", approval) {
		t.Fatal("approval unexpectedly had a consumer")
	}
	select {
	case count := <-replayed:
		if count == 0 {
			t.Fatal("follower wake happened before pending approval was made replayable")
		}
	case <-time.After(time.Second):
		t.Fatal("pending approval did not wake the follower")
	}
}

func TestCodexDesktopPendingApprovalReplaysAfterObserverUnregister(t *testing.T) {
	runtime, approval := desktopPendingApprovalRuntime(t)
	a := newACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}}, acpAgentOptions{})
	a.desktopRuntime = runtime
	observer := make(chan *codexTurnEvent, 1)
	observerID := a.registerTurnObserver("thread-1", observer)
	if !a.dispatchDesktopTurnEvent("thread-1", approval) {
		t.Fatal("approval was not queued for the frontend observer")
	}
	a.unregisterTurnObserver("thread-1", observerID, observer)

	replayed := findCodexDesktopActionEvent(runtime.replayActiveTurnEvents("thread-1"))
	if replayed == nil || replayed.Approval == nil || replayed.Approval.Request.RequestID != "request-1" {
		t.Fatalf("orphaned observer approval was not replayable: %#v", replayed)
	}
}

func TestCodexDesktopPendingApprovalReplaysWhenObserverDetachesDuringPrompt(t *testing.T) {
	runtime, approval := desktopPendingApprovalRuntime(t)
	a := newACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}}, acpAgentOptions{})
	a.desktopRuntime = runtime
	turnCh := make(chan *codexTurnEvent, 1)
	turnCh <- approval
	ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
		return "", ErrCodexObserverDetached
	})

	_, err := a.collectAttachedCodexTurn(ctx, codexThreadWatchOptions{
		threadID: "thread-1", targetTurnID: "turn-1",
		turnCh: turnCh, reconcile: make(chan time.Time),
	})
	if !errors.Is(err, ErrCodexObserverDetached) {
		t.Fatalf("watch error=%v, want observer detached", err)
	}
	replayed := findCodexDesktopActionEvent(runtime.replayActiveTurnEvents("thread-1"))
	if replayed == nil || replayed.Approval == nil || replayed.Approval.Request.RequestID != "request-1" {
		t.Fatalf("detached approval prompt was not replayable: %#v", replayed)
	}
}

func TestCodexDesktopPendingApprovalMovesToNextObserverAfterInFlightDetach(t *testing.T) {
	runtime, approval := desktopPendingApprovalRuntime(t)
	a := newACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}}, acpAgentOptions{})
	a.desktopRuntime = runtime
	first := make(chan *codexTurnEvent, 1)
	second := make(chan *codexTurnEvent, 1)
	firstID := a.registerTurnObserver("thread-1", first)
	secondID := a.registerTurnObserver("thread-1", second)
	defer a.unregisterTurnObserver("thread-1", secondID, second)
	if !a.dispatchDesktopTurnEvent("thread-1", approval) {
		t.Fatal("approval was not delivered to the first observer")
	}
	inFlight := <-first
	if inFlight.Approval == nil || inFlight.Approval.Request.RequestID != "request-1" {
		t.Fatalf("first observer received %#v", inFlight)
	}

	// 模拟首个飞书窗口在等待用户选择时被解除观察：collector 会先把交互
	// 恢复为可重放状态，再注销自己的 observer。
	a.abandonCodexTurnEvent("thread-1", inFlight)
	a.unregisterTurnObserver("thread-1", firstID, first)

	select {
	case replayed := <-second:
		if replayed.Approval == nil || replayed.Approval.Request.RequestID != "request-1" {
			t.Fatalf("second observer received %#v", replayed)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight approval was not handed to the remaining observer")
	}
	select {
	case duplicate := <-second:
		t.Fatalf("approval was delivered more than once: %#v", duplicate)
	default:
	}
}

func desktopPendingApprovalRuntime(t *testing.T) (*codexDesktopRuntime, *codexTurnEvent) {
	t.Helper()
	caller := &codexDesktopActionCaller{}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	state := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now, actions: actions})
	raw := desktopStateFixture("thread-1", "active")
	raw["turns"] = []any{desktopTurnFixture("turn-1", "inProgress", nil)}
	raw["requests"] = []any{desktopPendingRequestFixture(
		"request-1", "item/commandExecution/requestApproval",
	)}
	update, err := state.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &codexDesktopRuntime{state: state, actions: actions}, findCodexDesktopApprovalEvent(t, update.Events)
}

func desktopPendingRequestFixture(requestID string, method string) map[string]any {
	return map[string]any{
		"id": requestID, "method": method,
		"params": map[string]any{"availableDecisions": []any{"accept", "decline"}},
	}
}

func findCodexDesktopApprovalEvent(t *testing.T, events []*codexTurnEvent) *codexTurnEvent {
	t.Helper()
	event := findCodexDesktopActionEvent(events)
	if event == nil || event.Approval == nil {
		t.Fatalf("approval event not found in %#v", events)
	}
	return event
}

func findCodexDesktopActionEvent(events []*codexTurnEvent) *codexTurnEvent {
	for _, event := range events {
		if event.Approval != nil || event.UserInput != nil {
			return event
		}
	}
	return nil
}
