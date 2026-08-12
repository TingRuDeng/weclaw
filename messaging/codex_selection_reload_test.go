package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
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
	if migrated.Version != codexSessionStateVersion || len(migrated.Controls) != 0 {
		t.Fatalf("migrated version=%d controls=%#v", migrated.Version, migrated.Controls)
	}
}

func TestCodexSessionV11MigrationDropsFollowerWithoutAuthorizedIdentity(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	bindingKey := codexBindingKey("feishu:tenant:dm:chat:user", "codex")
	workspace := "/workspace/project-a"
	writeCodexSessionState(t, filePath, codexSessionState{
		Version: 11,
		Bindings: map[string]codexSessionBinding{
			bindingKey: {
				ActiveWorkspace: workspace,
				Workspaces:      map[string]codexWorkspaceSession{workspace: {ThreadID: "thread-a"}},
				FollowRevision:  7,
				Follower: &codexFrontendFollower{
					WorkspaceRoot: workspace, ThreadID: "thread-a", ActorUserID: "ou-user",
					DeliveryRoute: platform.DeliveryRoute{
						Platform: platform.PlatformFeishu, AccountID: "bot-a", ChatID: "chat-a", ReplyToID: "message-a",
					},
				},
				FollowTurnID: "turn-a", FollowTurnInitialized: true, FollowTurnPending: true,
			},
		},
	})

	store := newCodexSessionStore()
	store.SetFilePath(filePath)
	store.mu.Lock()
	binding := store.bindings[bindingKey]
	store.mu.Unlock()
	if binding.Follower != nil || binding.FollowRevision != 8 ||
		binding.FollowTurnID != "" || binding.FollowTurnInitialized || binding.FollowTurnPending {
		t.Fatalf("migrated binding=%#v", binding)
	}
	threadID, pending := store.getThread(bindingKey, workspace)
	if threadID != "thread-a" || pending {
		t.Fatalf("thread selection was not preserved: thread=%q pending=%v", threadID, pending)
	}
}
