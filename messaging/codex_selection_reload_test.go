package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexSessionV3MigrationKeepsBindingsAndDropsOwners(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	keyA := codexBindingKey("route-a", "codex")
	keyB := codexBindingKey("route-b", "codex")
	legacy := codexSessionState{
		Version: 3,
		Bindings: map[string]codexSessionBinding{
			keyA: {ActiveWorkspace: "/workspace/a", Workspaces: map[string]codexWorkspaceSession{"/workspace/a": {ThreadID: "thread-shared"}}},
			keyB: {ActiveWorkspace: "/workspace/b", Workspaces: map[string]codexWorkspaceSession{"/workspace/b": {ThreadID: "thread-shared"}}},
		},
		Controls: map[string]legacyCodexControlIntent{
			"thread-shared": {Owner: "remote", RouteBindingKey: keyA, ConversationID: "conversation-a", Revision: 9},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	store := newCodexSessionStore()
	store.SetFilePath(filePath)

	for key, workspace := range map[string]string{keyA: "/workspace/a", keyB: "/workspace/b"} {
		threadID, pending := store.getThread(key, workspace)
		if pending || threadID != "thread-shared" {
			t.Fatalf("binding %q thread=%q pending=%v", key, threadID, pending)
		}
	}
	data, err = os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	var migrated codexSessionState
	if err := json.Unmarshal(data, &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.Version != 4 || len(migrated.Controls) != 0 {
		t.Fatalf("migrated version=%d controls=%#v", migrated.Version, migrated.Controls)
	}
}
