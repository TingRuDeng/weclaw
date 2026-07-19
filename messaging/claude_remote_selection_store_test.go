package messaging

import (
	"errors"
	"testing"
)

func TestClaudeBindingSelectionAllowsMultipleRoutesOnSameSession(t *testing.T) {
	store := newClaudeSessionStore()
	for _, route := range []string{"route-a", "route-b"} {
		key := claudeBindingKey(route, "claude")
		snapshot := store.bindingSelectionSnapshot(key, "session-shared")
		mutation, err := store.commitBindingSelection(claudeBindingSelectionUpdate{
			BindingKey: key, WorkspaceRoot: "/workspace", TargetSessionID: "session-shared",
			BindingStatus: claudeBindingReady, Expected: snapshot,
		})
		if err != nil {
			t.Fatalf("route=%s commit: %v", route, err)
		}
		if mutation.Current.SessionID != "session-shared" || mutation.Current.Revision == 0 {
			t.Fatalf("route=%s mutation=%+v", route, mutation)
		}
	}
	if got := len(store.bindings); got != 2 {
		t.Fatalf("bindings=%d, want 2", got)
	}
}

func TestClaudeBindingSelectionRejectsStaleSnapshot(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	stale := store.bindingSelectionSnapshot(key, "session-a")
	if _, err := store.commitBindingSelection(claudeBindingSelectionUpdate{
		BindingKey: key, WorkspaceRoot: "/workspace", TargetSessionID: "session-a",
		BindingStatus: claudeBindingReady, Expected: stale,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.commitBindingSelection(claudeBindingSelectionUpdate{
		BindingKey: key, WorkspaceRoot: "/workspace", TargetSessionID: "session-a",
		BindingStatus: claudeBindingReady, Expected: stale,
	}); !errors.Is(err, errClaudeBindingSelectionChanged) {
		t.Fatalf("err=%v, want stale selection", err)
	}
}

func TestClaudeBindingSelectionPersistenceFailureDoesNotPublish(t *testing.T) {
	store := newClaudeSessionStore()
	store.persist = func(claudeSessionState) error { return errors.New("disk full") }
	key := claudeBindingKey("route-a", "claude")
	snapshot := store.bindingSelectionSnapshot(key, "session-a")
	if _, err := store.commitBindingSelection(claudeBindingSelectionUpdate{
		BindingKey: key, WorkspaceRoot: "/workspace", TargetSessionID: "session-a",
		BindingStatus: claudeBindingReady, Expected: snapshot,
	}); err == nil {
		t.Fatal("commit error=nil")
	}
	if _, ok := store.bindingSnapshot(key); ok {
		t.Fatalf("failed candidate leaked into live state: %+v", store.binding(key))
	}
}

func TestClaudeBindingMutationRollbackUsesAfterImageCAS(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	first, err := store.commitBindingSelection(claudeBindingSelectionUpdate{
		BindingKey: key, WorkspaceRoot: "/workspace", TargetSessionID: "session-a",
		BindingStatus: claudeBindingReady, Expected: store.bindingSelectionSnapshot(key, "session-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.rollbackBindingMutation(first); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.bindingSnapshot(key); ok {
		t.Fatalf("rollback left binding: %+v", store.binding(key))
	}

	second, err := store.commitBindingSelection(claudeBindingSelectionUpdate{
		BindingKey: key, WorkspaceRoot: "/workspace", TargetSessionID: "session-a",
		BindingStatus: claudeBindingReady, Expected: store.bindingSelectionSnapshot(key, "session-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.bindings[key] = newClaudeBinding("/workspace", "session-b", claudeBindingReady)
	if err := store.rollbackBindingMutation(second); !errors.Is(err, errClaudeBindingSelectionChanged) {
		t.Fatalf("err=%v, want CAS rejection", err)
	}
}

func TestClaudeBindingReleaseOnlyChangesCallingRoute(t *testing.T) {
	store := newClaudeSessionStore()
	first := claudeBindingKey("route-a", "claude")
	second := claudeBindingKey("route-b", "claude")
	store.bindings[first] = newClaudeBinding("/old", "session-shared", claudeBindingReady)
	store.bindings[second] = newClaudeBinding("/old", "session-shared", claudeBindingReady)
	snapshot := store.bindingSelectionSnapshot(first, "session-shared")
	if _, err := store.commitBindingRelease(claudeBindingReleaseUpdate{
		BindingKey: first, WorkspaceRoot: "/new", Expected: snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.binding(first); got.SessionID != "" || got.WorkspaceRoot != "/new" || got.Status != claudeBindingUnbound {
		t.Fatalf("first=%+v", got)
	}
	if got := store.binding(second); got.SessionID != "session-shared" {
		t.Fatalf("second route changed: %+v", got)
	}
}
