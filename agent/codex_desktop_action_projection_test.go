package agent

import (
	"context"
	"encoding/json"
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
