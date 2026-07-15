package messaging

import (
	"context"
	"strings"
	"testing"
)

func TestReleaseClaudeSelectionKeepsBindingAndClearsRemoteRuntime(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1,
	}
	fake.runtimeSessions = map[string]string{conversationID: "session-a"}
	route := claudeSessionRoute{
		Context: context.Background(), UserID: "user-1", AgentName: "claude", Agent: fake,
		WorkspaceRoot: workspace, BindingKey: key,
	}

	mutation, err := h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route: route, WorkspaceRoot: workspace, KeepSelection: true, Command: "owner local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.binding(key); got.SessionID != "session-a" || got.Status != claudeBindingReady {
		t.Fatalf("binding=%+v", got)
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal || got.Revision != 2 {
		t.Fatalf("intent=%+v", got)
	}
	if _, ok := fake.CurrentClaudeSession(conversationID); ok || len(mutation.Released) != 1 {
		t.Fatalf("runtime 未清理: mutation=%+v runtime=%+v", mutation, fake.runtimeSessions)
	}
}

func TestReleaseClaudeSelectionRejectsActiveOwnedSession(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1,
	}
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{owner: "user-1"})
	if !started {
		t.Fatal("活动任务登记失败")
	}
	defer h.finishActiveTask(conversationID, task)

	_, err := h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route:         claudeSessionRoute{Context: context.Background(), UserID: "user-1", AgentName: "claude", Agent: fake, BindingKey: key},
		WorkspaceRoot: workspace, Command: "cd",
	})
	if err == nil || !strings.Contains(err.Error(), "远程任务仍在执行") {
		t.Fatalf("err=%v", err)
	}
	if got := store.binding(key); got.SessionID != "session-a" {
		t.Fatalf("binding=%+v", got)
	}
}

func TestReleaseClaudeSelectionKeepSelectionRequiresCurrentSession(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	h.ensureClaudeSessions().bindings[key] = newClaudeBinding(workspace, "", claudeBindingUnbound)
	_, err := h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route:         claudeSessionRoute{Context: context.Background(), UserID: "user-1", AgentName: "claude", Agent: fake, BindingKey: key},
		WorkspaceRoot: workspace, KeepSelection: true, Command: "owner local",
	})
	if err == nil || !strings.Contains(err.Error(), "当前没有 Claude 会话") {
		t.Fatalf("err=%v", err)
	}
}

func TestReleaseClaudeSelectionWithoutSessionUpdatesWorkspace(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	other := t.TempDir()
	h.ensureClaudeSessions().bindings[key] = newClaudeBinding(workspace, "", claudeBindingUnbound)
	_, err := h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route:         claudeSessionRoute{Context: context.Background(), UserID: "user-1", AgentName: "claude", Agent: fake, BindingKey: key},
		WorkspaceRoot: other, Command: "cd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.ensureClaudeSessions().binding(key); got.WorkspaceRoot != normalizeClaudeWorkspaceRoot(other) || got.Status != claudeBindingUnbound {
		t.Fatalf("binding=%+v", got)
	}
}
