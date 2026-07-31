package messaging

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCodexRemoteThreadReleaseOnlyChangesCallerFrontend(t *testing.T) {
	store := newCodexSessionStore()
	workspace := "/workspace/shared"
	callerKey := codexBindingKey("route-a", "codex")
	otherKey := codexBindingKey("route-b", "codex")
	store.setThread(callerKey, workspace, "thread-shared")
	store.setThread(otherKey, workspace, "thread-shared")

	result, err := store.releaseRemoteThread(callerKey, "thread-shared")
	if err != nil {
		t.Fatal(err)
	}
	if !result.changed {
		t.Fatal("release changed=false")
	}
	if threadID, pending := store.getThread(callerKey, workspace); threadID != "" || pending {
		t.Fatalf("caller binding thread=%q pending=%v", threadID, pending)
	}
	if threadID, pending := store.getThread(otherKey, workspace); threadID != "thread-shared" || pending {
		t.Fatalf("other binding thread=%q pending=%v", threadID, pending)
	}
}

func TestCodexRemoteThreadReleaseRollbackPreservesOtherFrontendChanges(t *testing.T) {
	store := newCodexSessionStore()
	workspace := "/workspace/shared"
	callerKey := codexBindingKey("route-a", "codex")
	otherKey := codexBindingKey("route-b", "codex")
	store.setThread(callerKey, workspace, "thread-shared")
	store.setThread(otherKey, workspace, "thread-old")

	result, err := store.releaseRemoteThread(callerKey, "thread-shared")
	if err != nil {
		t.Fatal(err)
	}
	store.setThread(otherKey, workspace, "thread-new")
	if err := store.rollbackRemoteThreadRelease(result); err != nil {
		t.Fatal(err)
	}
	if threadID, _ := store.getThread(callerKey, workspace); threadID != "thread-shared" {
		t.Fatalf("caller binding=%q, want restored thread-shared", threadID)
	}
	if threadID, _ := store.getThread(otherKey, workspace); threadID != "thread-new" {
		t.Fatalf("other binding=%q, want concurrent thread-new", threadID)
	}
}

func TestCodexRemoteThreadReleaseWriteFailureKeepsLiveBinding(t *testing.T) {
	store := newCodexSessionStore()
	store.filePath = filepath.Join(t.TempDir(), "state.json")
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	store.setThread(key, workspace, "thread-old")
	store.writeState = func(string, []byte) error { return errors.New("disk full") }

	if _, err := store.releaseRemoteThread(key, "thread-old"); err == nil {
		t.Fatal("release error=nil")
	}
	if threadID, _ := store.getThread(key, workspace); threadID != "thread-old" {
		t.Fatalf("live binding=%q, want thread-old", threadID)
	}
}

func TestCodexRemoteThreadBindingKeysFindEveryFrontend(t *testing.T) {
	store := newCodexSessionStore()
	workspace := "/workspace/shared"
	first := codexBindingKey("route-a", "codex")
	second := codexBindingKey("route-b", "codex")
	store.setThread(first, workspace, "thread-shared")
	store.setThread(second, workspace, "thread-shared")
	store.setThread(codexBindingKey("route-c", "codex"), workspace, "thread-other")

	keys := store.remoteThreadBindingKeys("thread-shared")
	if len(keys) != 2 || keys[0] != first || keys[1] != second {
		t.Fatalf("binding keys=%q", keys)
	}
}

func TestCodexRemoteThreadArchiveTombstoneBlocksStaleSelectionUntilVisible(t *testing.T) {
	store := newCodexSessionStore()
	stateFile := filepath.Join(t.TempDir(), "state.json")
	store.SetFilePath(stateFile)
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	store.ensureWorkspace(key, workspace)
	store.markRemoteThreadArchived("thread-archived")

	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(stateFile)
	update := codexRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace,
		TargetThreadID: "thread-archived", ConversationID: "conversation-a",
		Expected: reloaded.remoteSelectionSnapshot(key, "thread-archived"),
	}
	if _, err := reloaded.commitRemoteSelection(update); !errors.Is(err, errCodexRemoteThreadArchived) {
		t.Fatalf("selection error=%v, want archived tombstone", err)
	}

	reloaded.reconcileVisibleRemoteThreads(map[string]bool{"thread-archived": true})
	visibleReloaded := newCodexSessionStore()
	visibleReloaded.SetFilePath(stateFile)
	update.Expected = visibleReloaded.remoteSelectionSnapshot(key, "thread-archived")
	if _, err := visibleReloaded.commitRemoteSelection(update); err != nil {
		t.Fatalf("selection after visible reconciliation: %v", err)
	}
}

func TestCodexRemoteThreadArchiveWriteFailureStillFailsClosedInMemory(t *testing.T) {
	store := newCodexSessionStore()
	store.filePath = filepath.Join(t.TempDir(), "state.json")
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	store.setThread(key, workspace, "thread-archived")
	store.writeState = func(string, []byte) error { return errors.New("disk full") }

	if err := store.markRemoteThreadArchived("thread-archived"); err == nil {
		t.Fatal("archive tombstone error=nil")
	}
	if threadID, pending := store.getThread(key, workspace); threadID != "" || pending {
		t.Fatalf("live binding after tombstone write failure thread=%q pending=%v", threadID, pending)
	}
	update := codexRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace,
		TargetThreadID: "thread-archived", ConversationID: "conversation-a",
		Expected: store.remoteSelectionSnapshot(key, "thread-archived"),
	}
	if _, err := store.commitRemoteSelection(update); !errors.Is(err, errCodexRemoteThreadArchived) {
		t.Fatalf("selection error=%v, want archived tombstone", err)
	}
}

func TestCodexRemoteThreadArchiveLoadClearsConflictingPersistedBinding(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	writeCodexSessionState(t, stateFile, codexSessionState{
		Version:  codexSessionStateVersion,
		Archived: []string{"thread-archived"},
		Bindings: map[string]codexSessionBinding{key: {
			ActiveWorkspace: workspace,
			Workspaces: map[string]codexWorkspaceSession{
				workspace: {ThreadID: "thread-archived"},
			},
		}},
	})

	store := newCodexSessionStore()
	store.SetFilePath(stateFile)

	if threadID, pending := store.getThread(key, workspace); threadID != "" || pending {
		t.Fatalf("loaded binding thread=%q pending=%v", threadID, pending)
	}
	update := codexRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace,
		TargetThreadID: "thread-archived", ConversationID: "conversation-a",
		Expected: store.remoteSelectionSnapshot(key, "thread-archived"),
	}
	if _, err := store.commitRemoteSelection(update); !errors.Is(err, errCodexRemoteThreadArchived) {
		t.Fatalf("selection error=%v, want archived tombstone", err)
	}
}

func TestCodexRemoteThreadArchiveTombstoneRejectsStaleSetThread(t *testing.T) {
	store := newCodexSessionStore()
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	if err := store.markRemoteThreadArchived("thread-archived"); err != nil {
		t.Fatal(err)
	}

	store.setThread(key, workspace, "thread-archived")

	store.mu.Lock()
	persistedThreadID := store.bindings[key].Workspaces[workspace].ThreadID
	store.mu.Unlock()
	if persistedThreadID != "" {
		t.Fatalf("stale setThread restored archived thread=%q", persistedThreadID)
	}
}

func TestCodexRemoteThreadArchiveTombstonePreventsReleaseRollback(t *testing.T) {
	store := newCodexSessionStore()
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	store.setThread(key, workspace, "thread-archived")
	release, err := store.releaseRemoteThread(key, "thread-archived")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.markRemoteThreadArchived("thread-archived"); err != nil {
		t.Fatal(err)
	}

	if err := store.rollbackRemoteThreadRelease(release); !errors.Is(err, errCodexRemoteThreadArchived) {
		t.Fatalf("rollback error=%v, want archived tombstone", err)
	}
	store.mu.Lock()
	persistedThreadID := store.bindings[key].Workspaces[workspace].ThreadID
	store.mu.Unlock()
	if persistedThreadID != "" {
		t.Fatalf("rollback restored archived thread=%q", persistedThreadID)
	}
}
