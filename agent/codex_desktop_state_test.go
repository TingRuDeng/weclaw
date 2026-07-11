package agent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestCodexDesktopStateRejectsPatchWithoutBaseline(t *testing.T) {
	requested := make(chan string, 1)
	store := newCodexDesktopStateStore(codexDesktopStateOptions{
		now:             time.Now,
		requestSnapshot: func(threadID string) { requested <- threadID },
	})

	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 0, revision: 1, patches: []codexDesktopPatch{
			{Op: "add", Path: []any{"id"}, Value: "thread-1"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	if update.Applied || !update.NeedsSnapshot {
		t.Fatalf("update = %#v", update)
	}
	if got := <-requested; got != "thread-1" {
		t.Fatalf("requested thread = %s", got)
	}
}

func TestCodexDesktopStateRejectsRevisionGap(t *testing.T) {
	requests := 0
	store := newCodexDesktopStateStore(codexDesktopStateOptions{
		now: time.Now,
		requestSnapshot: func(string) {
			requests++
		},
	})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 2, raw: desktopStateFixture("thread-1", "idle"),
	}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}

	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 3, revision: 4, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"threadRuntimeStatus", "type"}, Value: "running"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	if update.Applied || !update.NeedsSnapshot || requests != 1 {
		t.Fatalf("update = %#v, requests = %d", update, requests)
	}
	snapshot, _ := store.snapshot("thread-1")
	if snapshot.Revision != 2 || snapshot.State.Active {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestCodexDesktopStateDeduplicatesRevision(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 2, raw: desktopStateFixture("thread-1", "idle"),
	}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}

	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 2, revision: 2,
	})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	if update.Applied || update.NeedsSnapshot || len(update.Events) != 0 {
		t.Fatalf("duplicate update = %#v", update)
	}
}

func TestCodexDesktopStateAppliesBroadcastEnvelope(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	envelope := codexDesktopEnvelope{
		Type: codexDesktopEnvelopeBroadcast, Method: "thread-stream-state-changed", Version: 11,
		Params: json.RawMessage(`{
			"conversationId":"thread-1",
			"change":{"type":"snapshot","revision":7,"conversationState":{
				"id":"thread-1","turns":[],"requests":[],"latestModel":"gpt-live",
				"latestReasoningEffort":"medium","threadRuntimeStatus":{"type":"idle"}
			}}
		}`),
	}

	update, err := store.applyEnvelope(3, envelope)
	if err != nil {
		t.Fatalf("applyEnvelope() error = %v", err)
	}
	if update.Snapshot.ConnectionEpoch != 3 || update.Snapshot.Revision != 7 {
		t.Fatalf("snapshot = %#v", update.Snapshot)
	}
	if update.Snapshot.State.Model != "gpt-live" || update.Snapshot.State.Effort != "medium" {
		t.Fatalf("state = %#v", update.Snapshot.State)
	}
}

func TestCodexDesktopStateSnapshotIsDeepCopy(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopStateFixture("thread-1", "idle")
	raw["requests"] = []any{map[string]any{"id": 1, "method": "item/tool/call"}}
	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	update.Snapshot.Raw["latestModel"] = "mutated"
	raw["latestModel"] = "input-mutated"

	snapshot, ok := store.snapshot("thread-1")
	if !ok || snapshot.State.Model != "gpt-test" || snapshot.Raw["latestModel"] != "gpt-test" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if _, ok := snapshot.Requests["1"]; !ok {
		t.Fatalf("requests = %#v", snapshot.Requests)
	}
}

func TestCodexDesktopStateClonesQueuedPatches(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	patches := []codexDesktopPatch{
		{Op: "replace", Path: []any{"threadRuntimeStatus", "type"}, Value: "running"},
	}
	if _, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 0, revision: 1, patches: patches,
	}); err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	patches[0].Path[0] = "corrupted"

	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 0, raw: desktopStateFixture("thread-1", "idle"),
	})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	status := update.Snapshot.Raw["threadRuntimeStatus"].(map[string]any)["type"]
	if status != "running" || update.Snapshot.Revision != 1 {
		t.Fatalf("snapshot = %#v", update.Snapshot)
	}
}

func TestCodexDesktopStateIgnoresStaleConnectionEpoch(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 2, revision: 1, raw: desktopStateFixture("thread-1", "idle"),
	}); err != nil {
		t.Fatalf("applySnapshot(new epoch) error = %v", err)
	}
	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 99, raw: desktopStateFixture("thread-1", "running"),
	})
	if err != nil {
		t.Fatalf("applySnapshot(stale epoch) error = %v", err)
	}
	if update.Applied || update.Snapshot.ConnectionEpoch != 2 || update.Snapshot.Revision != 1 {
		t.Fatalf("stale update = %#v", update)
	}
}

func TestCodexDesktopStateKeepsOnlyPendingRequests(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopStateFixture("thread-1", "idle")
	raw["requests"] = []any{
		map[string]any{"id": "pending", "method": "item/fileChange/requestApproval"},
		map[string]any{"id": "done", "method": "item/tool/requestUserInput", "status": "resolved"},
	}
	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	if len(update.Snapshot.Requests) != 1 || !update.Snapshot.State.WaitingOnApproval {
		t.Fatalf("snapshot = %#v", update.Snapshot)
	}
	if update.Snapshot.State.WaitingOnUserInput {
		t.Fatalf("state = %#v", update.Snapshot.State)
	}
}

func TestCodexDesktopStateRejectsMismatchedConversationID(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	_, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1,
		raw: desktopStateFixture("thread-other", "idle"),
	})
	if err == nil {
		t.Fatal("applySnapshot() error = nil")
	}
	if store.threadCount() != 0 {
		t.Fatal("不匹配 snapshot 被写入缓存")
	}
}

func TestCodexDesktopStateRejectsPatchChangingConversationID(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: desktopStateFixture("thread-1", "idle"),
	}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	_, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2,
		patches: []codexDesktopPatch{{Op: "replace", Path: []any{"id"}, Value: "thread-other"}},
	})
	if err == nil {
		t.Fatal("applyPatchSet() error = nil")
	}
	snapshot, _ := store.snapshot("thread-1")
	if snapshot.Revision != 1 || snapshot.Raw["id"] != "thread-1" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestCodexDesktopStateCapsQueuedPatchCount(t *testing.T) {
	const oversizedPatchCount = 301
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	patches := make([]codexDesktopPatch, oversizedPatchCount)
	for index := range patches {
		patches[index] = codexDesktopPatch{
			Op: "add", Path: []any{fmt.Sprintf("field-%d", index)}, Value: index,
		}
	}
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 0, revision: 1, patches: patches,
	})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	store.mu.Lock()
	queued := len(store.queued["thread-1"])
	store.mu.Unlock()
	if queued != 0 || !update.NeedsSnapshot {
		t.Fatalf("queued sets = %d, update = %#v", queued, update)
	}
}

func desktopStateFixture(threadID string, runtimeStatus string) map[string]any {
	return map[string]any{
		"id":                    threadID,
		"turns":                 []any{},
		"requests":              []any{},
		"latestModel":           "gpt-test",
		"latestReasoningEffort": "high",
		"threadRuntimeStatus":   map[string]any{"type": runtimeStatus},
	}
}
