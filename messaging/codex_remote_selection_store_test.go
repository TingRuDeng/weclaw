package messaging

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCodexRemoteSelectionPersistsFrontendBindingOnly(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	store := newCodexSessionStore()
	store.SetFilePath(filePath)
	workspace := "/workspace/a"
	bindingKey := codexBindingKey("route-a", "codex")
	store.ensureWorkspace(bindingKey, workspace)
	snapshot := store.remoteSelectionSnapshot(bindingKey, "thread-shared")

	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspace,
		TargetThreadID: "thread-shared", ConversationID: "conversation-a",
		Expected: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if threadID, pending := store.getThread(bindingKey, workspace); pending || threadID != "thread-shared" {
		t.Fatalf("binding thread=%q pending=%v", threadID, pending)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	var state codexSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != 4 || len(state.Controls) != 0 {
		t.Fatalf("persisted state version=%d controls=%#v", state.Version, state.Controls)
	}
}

func TestCodexRemoteSelectionAllowsMultipleFrontendsOnSameThread(t *testing.T) {
	store := newCodexSessionStore()
	workspace := "/workspace/shared"
	keys := []string{codexBindingKey("route-a", "codex"), codexBindingKey("route-b", "codex")}
	updates := make([]codexRemoteSelectionUpdate, 0, len(keys))
	for index, key := range keys {
		store.ensureWorkspace(key, workspace)
		updates = append(updates, codexRemoteSelectionUpdate{
			BindingKey: key, WorkspaceRoot: workspace,
			TargetThreadID: "thread-shared", ConversationID: "conversation-" + string(rune('a'+index)),
			Expected: store.remoteSelectionSnapshot(key, "thread-shared"),
		})
	}

	start := make(chan struct{})
	errs := make(chan error, len(updates))
	var wg sync.WaitGroup
	for _, update := range updates {
		update := update
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.commitRemoteSelection(update)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("multi-frontend selection error=%v", err)
		}
	}
	for _, key := range keys {
		threadID, pending := store.getThread(key, workspace)
		if pending || threadID != "thread-shared" {
			t.Fatalf("binding %q thread=%q pending=%v", key, threadID, pending)
		}
	}
}

func TestCodexRemoteSelectionRejectsStaleSameFrontendBinding(t *testing.T) {
	store := newCodexSessionStore()
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	store.setThread(key, workspace, "thread-old")
	snapshot := store.remoteSelectionSnapshot(key, "thread-target")
	store.setThread(key, workspace, "thread-newer")

	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace,
		TargetThreadID: "thread-target", ConversationID: "conversation-a",
		Expected: snapshot,
	})
	if !errors.Is(err, errCodexRemoteSelectionChanged) {
		t.Fatalf("error=%v, want stale binding", err)
	}
}

func TestCodexRemoteSelectionWriteFailureKeepsLiveBinding(t *testing.T) {
	store := newCodexSessionStore()
	store.filePath = filepath.Join(t.TempDir(), "state.json")
	store.writeState = func(string, []byte) error { return errors.New("disk full") }
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	store.setThread(key, workspace, "thread-old")
	snapshot := store.remoteSelectionSnapshot(key, "thread-target")

	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace,
		TargetThreadID: "thread-target", ConversationID: "conversation-a",
		Expected: snapshot,
	})
	if err == nil {
		t.Fatal("commit error=nil")
	}
	if threadID, _ := store.getThread(key, workspace); threadID != "thread-old" {
		t.Fatalf("live binding=%q, want thread-old", threadID)
	}
}
