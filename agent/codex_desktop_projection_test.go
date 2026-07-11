package agent

import (
	"fmt"
	"testing"
	"time"
)

func TestCodexDesktopProjectionFindsExplicitActiveTurn(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "inProgress", nil),
	})

	update, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	if !update.Snapshot.State.Active || update.Snapshot.State.ActiveTurnID != "turn-1" {
		t.Fatalf("state = %#v", update.Snapshot.State)
	}
	if update.Snapshot.State.Model != "gpt-test" || update.Snapshot.State.Effort != "high" {
		t.Fatalf("model state = %#v", update.Snapshot.State)
	}
	assertCodexDesktopEvent(t, update.Events, "started", "turn-1")
}

func TestCodexDesktopProjectionDoesNotTreatUnknownStatusAsActive(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "mystery", nil),
	})

	update, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	if update.Snapshot.State.Active || update.Snapshot.State.ActiveTurnID != "" {
		t.Fatalf("state = %#v", update.Snapshot.State)
	}
	if len(update.Events) != 0 {
		t.Fatalf("events = %#v", update.Events)
	}
}

func TestCodexDesktopProjectionDoesNotEmitEmptyAgentText(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	items := []any{map[string]any{
		"id": "item-1", "type": "agentMessage", "status": "inProgress", "text": "",
	}}
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "inProgress", items),
	})
	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	if len(update.Events) != 1 || update.Events[0].Kind != "started" {
		t.Fatalf("events = %#v", update.Events)
	}
}

func TestCodexDesktopProjectionEmitsTextSuffixOnly(t *testing.T) {
	store := desktopProjectionStoreWithAgentText(t, "Hello")
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "items", 0, "text"}, Value: "Hello world"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	event := assertCodexDesktopEvent(t, update.Events, "", "turn-1")
	if event.ItemID != "item-1" || event.Delta != " world" || event.Text != "" {
		t.Fatalf("event = %#v", event)
	}
}

func TestCodexDesktopProjectionRebuildsRewrittenText(t *testing.T) {
	store := desktopProjectionStoreWithAgentText(t, "Hello")
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "items", 0, "text"}, Value: "Rewritten"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	event := assertCodexDesktopEvent(t, update.Events, "", "turn-1")
	if event.ItemID != "item-1" || event.Delta != "" || event.Text != "Rewritten" {
		t.Fatalf("event = %#v", event)
	}
}

func TestCodexDesktopProjectionEmitsTerminalOncePerTurn(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "inProgress", nil),
	})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "status"}, Value: "completed"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	assertCodexDesktopEvent(t, update.Events, "completed", "turn-1")

	update, err = store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 3, raw: update.Snapshot.Raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	if len(update.Events) != 0 {
		t.Fatalf("duplicate terminal events = %#v", update.Events)
	}
}

func TestCodexDesktopProjectionKeepsParallelSiblingTurnsSeparate(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "running", nil),
		desktopTurnFixture("turn-2", "processing", nil),
	})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "status"}, Value: "completed"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	assertCodexDesktopEvent(t, update.Events, "completed", "turn-1")
	if !update.Snapshot.State.Active || update.Snapshot.State.ActiveTurnID != "turn-2" {
		t.Fatalf("state = %#v", update.Snapshot.State)
	}
}

func TestCodexDesktopProjectionEmitsItemCompletedAndProgress(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	items := []any{
		map[string]any{"id": "agent-1", "type": "agentMessage", "status": "inProgress", "text": "Done"},
		map[string]any{"id": "command-1", "type": "commandExecution", "status": "inProgress", "aggregatedOutput": "building"},
	}
	raw := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "running", items)})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "items", 0, "status"}, Value: "completed"},
			{Op: "replace", Path: []any{"turns", 0, "items", 1, "aggregatedOutput"}, Value: "built"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	assertCodexDesktopEvent(t, update.Events, "item_completed", "turn-1")
	progress := assertCodexDesktopEvent(t, update.Events, "progress", "turn-1")
	if progress.Progress == nil || progress.Progress.Kind != "command" || progress.Progress.Action != "" {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestCodexDesktopStateEvictsOnlyIdleThreads(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	active := desktopProjectionFixture("active", []any{desktopTurnFixture("turn-active", "running", nil)})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "active", epoch: 1, revision: 1, raw: active}); err != nil {
		t.Fatalf("applySnapshot(active) error = %v", err)
	}
	for index := 0; index < codexDesktopMaxThreads; index++ {
		threadID := fmt.Sprintf("idle-%03d", index)
		raw := desktopProjectionFixture(threadID, nil)
		if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: threadID, epoch: 1, revision: 1, raw: raw}); err != nil {
			t.Fatalf("applySnapshot(%s) error = %v", threadID, err)
		}
	}
	if _, ok := store.snapshot("active"); !ok {
		t.Fatal("active thread 被淘汰")
	}
	if count := store.threadCount(); count != codexDesktopMaxThreads {
		t.Fatalf("thread count = %d", count)
	}
}

func desktopProjectionStoreWithAgentText(t *testing.T, text string) *codexDesktopStateStore {
	t.Helper()
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	items := []any{map[string]any{
		"id": "item-1", "type": "agentMessage", "status": "inProgress", "text": text,
	}}
	raw := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "inProgress", items)})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	return store
}

func desktopProjectionFixture(threadID string, turns []any) map[string]any {
	return map[string]any{
		"id": threadID, "turns": turns, "requests": []any{},
		"latestModel": "gpt-test", "latestReasoningEffort": "high",
		"threadRuntimeStatus": map[string]any{"type": "idle"},
	}
}

func desktopTurnFixture(turnID string, status string, items []any) map[string]any {
	return map[string]any{"turnId": turnID, "status": status, "items": items}
}

func assertCodexDesktopEvent(t *testing.T, events []*codexTurnEvent, kind string, turnID string) *codexTurnEvent {
	t.Helper()
	for _, event := range events {
		if event.Kind == kind && event.TurnID == turnID {
			return event
		}
	}
	t.Fatalf("event kind=%q turn=%q not found in %#v", kind, turnID, events)
	return nil
}
