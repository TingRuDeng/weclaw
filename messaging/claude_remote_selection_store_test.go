package messaging

import (
	"errors"
	"testing"
)

func TestClaudeCommitRemoteSelectionAtomicallySwapsAForB(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key,
		ConversationID: buildClaudeConversationID("route-a", "claude", workspace), Revision: 3,
	}
	expected := store.remoteSelectionSnapshot(key, "session-b")
	mutation, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-b",
		ConversationID: buildClaudeConversationID("route-a", "claude", workspace), Expected: expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.binding(key); got.SessionID != "session-b" {
		t.Fatalf("binding=%+v", got)
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal || got.Revision != 4 {
		t.Fatalf("old=%+v", got)
	}
	if got := store.controlIntent("session-b"); got.Owner != claudeOwnerRemote || got.BindingKey != key || got.Revision != 1 {
		t.Fatalf("target=%+v", got)
	}
	if mutation.Before.Bindings[key].SessionID != "session-a" || mutation.After.Bindings[key].SessionID != "session-b" {
		t.Fatalf("mutation=%+v", mutation)
	}
}

func TestClaudeCommitRemoteSelectionPersistsOwnerWhenRuntimeUnavailable(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	mutation, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-b",
		ConversationID: "conversation-b", BindingStatus: claudeBindingResumeFailed,
		Expected: store.remoteSelectionSnapshot(key, "session-b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.binding(key); got.SessionID != "session-b" || got.Status != claudeBindingResumeFailed {
		t.Fatalf("binding=%+v", got)
	}
	if got := store.controlIntent("session-b"); got.Owner != claudeOwnerRemote || got.BindingKey != key {
		t.Fatalf("control=%+v", got)
	}
	if got := mutation.After.Bindings[key]; got.Status != claudeBindingResumeFailed {
		t.Fatalf("mutation=%+v", mutation)
	}
}

func TestClaudeCommitRemoteSelectionRejectsOtherRoute(t *testing.T) {
	store := newClaudeSessionStore()
	ownerKey := claudeBindingKey("route-owner", "claude")
	requestKey := claudeBindingKey("route-request", "claude")
	store.controls["session-b"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: ownerKey, ConversationID: "owner-conversation", Revision: 2,
	}
	expected := store.remoteSelectionSnapshot(requestKey, "session-b")
	_, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: requestKey, WorkspaceRoot: t.TempDir(), TargetSessionID: "session-b",
		ConversationID: "request-conversation", Expected: expected,
	})
	if !errors.Is(err, errClaudeRemoteSelectionOtherRoute) {
		t.Fatalf("error=%v", err)
	}
}

func TestClaudeCommitRemoteSelectionSaveFailureKeepsLiveState(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "conversation-a", Revision: 2}
	store.persist = func(claudeSessionState) error { return errors.New("disk full") }
	beforeControl := store.controlIntent("session-a")
	expected := store.remoteSelectionSnapshot(key, "session-b")
	_, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-b",
		ConversationID: "conversation-b", Expected: expected,
	})
	if err == nil || store.binding(key).SessionID != "session-a" {
		t.Fatalf("error=%v binding=%+v", err, store.binding(key))
	}
	if got := store.controlIntent("session-a"); got != beforeControl {
		t.Fatalf("未落盘状态不应发布: %+v", got)
	}
}

func TestClaudeCommitRemoteSelectionUsesCompleteExpectedCAS(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*claudeSessionStore, string)
	}{
		{name: "binding", mutate: func(store *claudeSessionStore, key string) {
			binding := store.bindings[key]
			binding.Status = claudeBindingPendingResume
			store.bindings[key] = binding
		}},
		{name: "target", mutate: func(store *claudeSessionStore, _ string) {
			store.controls["session-b"] = claudeControlIntent{Owner: claudeOwnerLocal, Revision: 1}
		}},
		{name: "route-owned", mutate: func(store *claudeSessionStore, key string) {
			store.controls["session-c"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "conversation-c", Revision: 1}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newClaudeSessionStore()
			key := claudeBindingKey("route-a", "claude")
			workspace := t.TempDir()
			store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
			store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "conversation-a", Revision: 1}
			expected := store.remoteSelectionSnapshot(key, "session-b")
			test.mutate(store, key)
			_, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
				BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-b",
				ConversationID: "conversation-b", Expected: expected,
			})
			if !errors.Is(err, errClaudeRemoteSelectionChanged) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestClaudeCommitRemoteSelectionDoesNotIncrementUnchangedTuple(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "conversation-a", Revision: 7}
	mutation, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-a", ConversationID: "conversation-a",
		Expected: store.remoteSelectionSnapshot(key, "session-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Target.Revision != 7 || store.controlIntent("session-a").Revision != 7 {
		t.Fatalf("mutation=%+v", mutation)
	}
}

func TestClaudeCommitRemoteReleaseReleasesRouteAndCanClearSelection(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "conversation-a", Revision: 2}
	mutation, err := store.commitRemoteRelease(claudeRemoteReleaseUpdate{
		BindingKey: key, WorkspaceRoot: workspace, KeepSelection: false,
		Expected: store.remoteSelectionSnapshot(key, "session-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.binding(key); got.SessionID != "" || got.Status != claudeBindingUnbound {
		t.Fatalf("binding=%+v", got)
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal || got.Revision != 3 {
		t.Fatalf("control=%+v", got)
	}
	if mutation.Released["session-a"].Owner != claudeOwnerLocal {
		t.Fatalf("mutation=%+v", mutation)
	}
}

func TestClaudeCommitRemoteReleaseUsesCASAndDoesNotPublishSaveFailure(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "conversation-a", Revision: 2,
	}
	expected := store.remoteSelectionSnapshot(key, "session-a")
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "conversation-a", Revision: 3,
	}
	if _, err := store.commitRemoteRelease(claudeRemoteReleaseUpdate{
		BindingKey: key, WorkspaceRoot: workspace, KeepSelection: true, Expected: expected,
	}); !errors.Is(err, errClaudeRemoteSelectionChanged) {
		t.Fatalf("CAS error=%v", err)
	}

	expected = store.remoteSelectionSnapshot(key, "session-a")
	beforeBinding := store.binding(key)
	beforeControl := store.controlIntent("session-a")
	store.persist = func(claudeSessionState) error { return errors.New("disk full") }
	if _, err := store.commitRemoteRelease(claudeRemoteReleaseUpdate{
		BindingKey: key, WorkspaceRoot: workspace, KeepSelection: true, Expected: expected,
	}); err == nil {
		t.Fatal("save failure error=nil")
	}
	if got := store.binding(key); got != beforeBinding {
		t.Fatalf("binding=%+v want=%+v", got, beforeBinding)
	}
	if got := store.controlIntent("session-a"); got != beforeControl {
		t.Fatalf("control=%+v want=%+v", got, beforeControl)
	}
}

func TestClaudeRemoteSelectionSnapshotsAndMutationsAreDeepCopies(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "conversation-a", Revision: 1,
	}
	snapshot := store.remoteSelectionSnapshot(key, "session-b")
	delete(snapshot.RouteOwned, "session-a")
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerRemote {
		t.Fatalf("snapshot mutation changed store: %+v", got)
	}

	mutation, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-b", ConversationID: "conversation-b",
		Expected: store.remoteSelectionSnapshot(key, "session-b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	delete(mutation.After.Controls, "session-b")
	mutation.Before.Bindings[key] = claudeSessionBinding{}
	if got := store.controlIntent("session-b"); got.Owner != claudeOwnerRemote {
		t.Fatalf("mutation changed control: %+v", got)
	}
	if got := store.binding(key); got.SessionID != "session-b" {
		t.Fatalf("mutation changed binding: %+v", got)
	}
}

func TestClaudeRollbackRemoteMutationOnlyWhenAfterMatches(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "conversation-a", Revision: 1}
	mutation, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-b", ConversationID: "conversation-b",
		Expected: store.remoteSelectionSnapshot(key, "session-b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.rollbackRemoteMutation(mutation); err != nil {
		t.Fatal(err)
	}
	if got := store.binding(key); got.SessionID != "session-a" {
		t.Fatalf("binding=%+v", got)
	}

	mutation, err = store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-b", ConversationID: "conversation-b",
		Expected: store.remoteSelectionSnapshot(key, "session-b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.controls["session-b"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "newer", Revision: mutation.Target.Revision + 1}
	if err := store.rollbackRemoteMutation(mutation); !errors.Is(err, errClaudeRemoteSelectionChanged) {
		t.Fatalf("error=%v", err)
	}
	if got := store.controlIntent("session-b"); got.ConversationID != "newer" {
		t.Fatalf("rollback 覆盖了并发状态: %+v", got)
	}
}
