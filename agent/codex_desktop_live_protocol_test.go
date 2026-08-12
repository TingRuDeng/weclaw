package agent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestCodexDesktopStateProjectsActiveTurnFromHistoryIsland(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopStateFixture("thread-1", "active")
	raw["turnHistory"] = map[string]any{"history": map[string]any{
		"entitiesByKey": map[string]any{
			"tail:1:local:active": map[string]any{
				"turnId": "turn-active", "status": "inProgress", "items": []any{},
			},
		},
		"islands": []any{map[string]any{
			"id": "tail:1", "entries": []any{map[string]any{
				"key": "turn:turn-active", "value": "tail:1:local:active",
			}},
		}},
	}}

	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	if !update.Snapshot.State.Active || update.Snapshot.State.ActiveTurnID != "turn-active" {
		t.Fatalf("state = %#v", update.Snapshot.State)
	}
}

func TestCodexDesktopStateIgnoresReadStateBroadcast(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	envelope := codexDesktopEnvelope{
		Type: codexDesktopEnvelopeBroadcast, Method: "thread-read-state-changed", Version: 1,
		Params: json.RawMessage(`{"conversationId":"thread-1","hasUnreadTurn":false}`),
	}

	update, err := store.applyEnvelope(1, envelope)
	if err != nil {
		t.Fatalf("applyEnvelope() error = %v", err)
	}
	if update.Applied {
		t.Fatalf("update = %#v", update)
	}
}

func TestCodexDesktopStateIgnoresStreamFollowingBroadcast(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	envelope := codexDesktopEnvelope{
		Type: codexDesktopEnvelopeBroadcast, Method: "thread-stream-following-changed", Version: 1,
		Params: json.RawMessage(`{"conversationId":"thread-1","hostId":"host-1","following":false}`),
	}

	update, err := store.applyEnvelope(1, envelope)
	if err != nil {
		t.Fatalf("applyEnvelope() error = %v", err)
	}
	if update.Applied {
		t.Fatalf("update = %#v", update)
	}
}

func TestCodexDesktopStateIgnoresStreamFollowingStatusRequest(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	envelope := codexDesktopEnvelope{
		Type: codexDesktopEnvelopeBroadcast, Method: "thread-stream-following-status-requested", Version: 1,
		Params: json.RawMessage(`{"conversationId":"thread-1","hostId":"host-1"}`),
	}

	update, err := store.applyEnvelope(1, envelope)
	if err != nil {
		t.Fatalf("applyEnvelope() error = %v", err)
	}
	if update.Applied {
		t.Fatalf("update = %#v", update)
	}
}

func TestCodexDesktopStateProjectsQueuedFollowupsBroadcast(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: desktopStateFixture("thread-1", "idle"),
	}); err != nil {
		t.Fatal(err)
	}
	envelope := codexDesktopEnvelope{
		Type: codexDesktopEnvelopeBroadcast, Method: "thread-queued-followups-changed", Version: 1,
		Params: json.RawMessage(`{"conversationId":"thread-1","messages":[{"text":"local draft"}]}`),
	}

	update, err := store.applyEnvelope(1, envelope)
	if err != nil {
		t.Fatalf("applyEnvelope() error = %v", err)
	}
	if !update.Applied || len(update.Snapshot.QueuedFollowUps) != 1 ||
		string(update.Snapshot.QueuedFollowUps[0]) != `{"text":"local draft"}` {
		t.Fatalf("update = %#v, want preserved local draft", update)
	}

	stale := envelope
	stale.Params = json.RawMessage(`{"conversationId":"thread-1","messages":[]}`)
	if _, err := store.applyEnvelope(0, stale); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.snapshot("thread-1")
	if len(snapshot.QueuedFollowUps) != 1 {
		t.Fatalf("stale epoch replaced queued follow-ups: %#v", snapshot.QueuedFollowUps)
	}
}

func TestCodexDesktopStateBoundsQueuedFollowupsWithoutSnapshot(t *testing.T) {
	now := time.Unix(0, 0)
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: func() time.Time {
		now = now.Add(time.Second)
		return now
	}})
	for index := 0; index < codexDesktopMaxThreads+2; index++ {
		threadID := fmt.Sprintf("thread-%03d", index)
		envelope := codexDesktopEnvelope{
			Type: codexDesktopEnvelopeBroadcast, Method: "thread-queued-followups-changed", Version: 1,
			Params: json.RawMessage(fmt.Sprintf(`{"conversationId":%q,"messages":[{"text":"draft"}]}`, threadID)),
		}
		if _, err := store.applyEnvelope(1, envelope); err != nil {
			t.Fatalf("applyEnvelope(%s) error = %v", threadID, err)
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.followUps) != codexDesktopMaxThreads {
		t.Fatalf("follow-ups count = %d, want %d", len(store.followUps), codexDesktopMaxThreads)
	}
	if _, exists := store.followUps["thread-000"]; exists {
		t.Fatal("oldest orphan follow-ups should be evicted")
	}
	newest := fmt.Sprintf("thread-%03d", codexDesktopMaxThreads+1)
	if _, exists := store.followUps[newest]; !exists {
		t.Fatalf("current orphan follow-ups %s should be retained", newest)
	}
}

func TestCodexDesktopStateIgnoresVersionlessClientStatusBroadcast(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	envelope := codexDesktopEnvelope{
		Type: codexDesktopEnvelopeBroadcast, Method: "client-status-changed",
		Params: json.RawMessage(`{"clientId":"desktop-1","status":"connected"}`),
	}

	update, err := store.applyEnvelope(1, envelope)
	if err != nil {
		t.Fatalf("applyEnvelope() error = %v", err)
	}
	if update.Applied {
		t.Fatalf("update = %#v", update)
	}
}

// TestCodexDesktopStateKeepsFinalCandidateAfterHistoryArchive 验证长会话分两次归档时
// 不会因为普通状态 revision 提前结束并丢失随后到达的 final_answer。
func TestCodexDesktopStateKeepsFinalCandidateAfterHistoryArchive(t *testing.T) {
	activeRaw := desktopHistoryTurnFixture("tail:1:local:active", "turn-active", "inProgress")
	_, _, activeProjection, _ := projectCodexDesktopSnapshot("thread-1", activeRaw, nil)

	emptyRaw := desktopStateFixture("thread-1", "idle")
	_, _, archivedProjection, removedEvents := projectCodexDesktopSnapshot("thread-1", emptyRaw, &activeProjection)
	if len(removedEvents) != 0 {
		t.Fatalf("removed events = %#v", removedEvents)
	}

	completedRaw := desktopHistoryTurnFixture("turn:turn-active", "turn-active", "completed")
	_, _, completedProjection, completedEvents := projectCodexDesktopSnapshot("thread-1", completedRaw, &archivedProjection)
	if len(completedEvents) != 0 {
		t.Fatalf("status-only completed must wait for final answer, events=%#v", completedEvents)
	}
	_, _, pendingProjection, settledEvents := projectCodexDesktopSnapshot("thread-1", completedRaw, &completedProjection)
	if len(settledEvents) != 0 || !pendingProjection.terminalCandidates["turn-active"] {
		t.Fatalf("ordinary completed revision settled before final answer: events=%#v candidates=%#v", settledEvents, pendingProjection.terminalCandidates)
	}
}

// desktopHistoryTurnFixture 构造 Codex Desktop 长会话中的单 turn 历史状态。
func desktopHistoryTurnFixture(entityKey string, turnID string, status string) map[string]any {
	raw := desktopStateFixture("thread-1", "active")
	raw["turnHistory"] = map[string]any{"history": map[string]any{
		"entitiesByKey": map[string]any{entityKey: map[string]any{
			"turnId": turnID, "status": status, "items": []any{},
		}},
		"islands": []any{map[string]any{
			"id": "tail:1", "entries": []any{map[string]any{
				"key": "turn:" + turnID, "value": entityKey,
			}},
		}},
	}}
	return raw
}
