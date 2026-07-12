package agent

import (
	"encoding/json"
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

func TestCodexDesktopStateIgnoresQueuedFollowupsBroadcast(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	envelope := codexDesktopEnvelope{
		Type: codexDesktopEnvelopeBroadcast, Method: "thread-queued-followups-changed", Version: 1,
		Params: json.RawMessage(`{"conversationId":"thread-1","messages":[]}`),
	}

	update, err := store.applyEnvelope(1, envelope)
	if err != nil {
		t.Fatalf("applyEnvelope() error = %v", err)
	}
	if update.Applied {
		t.Fatalf("update = %#v", update)
	}
}

// TestCodexDesktopStateCompletesTurnAfterHistoryArchive 验证长会话分两次归档时不会丢失终态。
func TestCodexDesktopStateCompletesTurnAfterHistoryArchive(t *testing.T) {
	activeRaw := desktopHistoryTurnFixture("tail:1:local:active", "turn-active", "inProgress")
	_, _, activeProjection, _ := projectCodexDesktopSnapshot("thread-1", activeRaw, nil)

	emptyRaw := desktopStateFixture("thread-1", "idle")
	_, _, archivedProjection, removedEvents := projectCodexDesktopSnapshot("thread-1", emptyRaw, &activeProjection)
	if len(removedEvents) != 0 {
		t.Fatalf("removed events = %#v", removedEvents)
	}

	completedRaw := desktopHistoryTurnFixture("turn:turn-active", "turn-active", "completed")
	_, _, _, completedEvents := projectCodexDesktopSnapshot("thread-1", completedRaw, &archivedProjection)
	assertCodexDesktopEvent(t, completedEvents, "completed", "turn-active")
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
