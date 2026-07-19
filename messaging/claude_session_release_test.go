package messaging

import (
	"context"
	"strings"
	"testing"
)

func TestReleaseClaudeSelectionUnbindsOnlyRouteAndClearsItsRuntime(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	first := claudeBindingKey("user-1", "claude")
	second := claudeBindingKey("user-2", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[first] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.bindings[second] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	fake.runtimeSessions = map[string]string{conversationID: "session-a"}
	route := claudeSessionRoute{
		Context: context.Background(), ActorUserID: "user-1", UserID: "user-1",
		AgentName: "claude", Agent: fake, WorkspaceRoot: workspace, BindingKey: first,
	}

	if _, err := h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route: route, WorkspaceRoot: workspace, Command: "cd",
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.binding(first); got.SessionID != "" || got.Status != claudeBindingUnbound {
		t.Fatalf("first=%+v", got)
	}
	if got := store.binding(second); got.SessionID != "session-a" {
		t.Fatalf("second changed=%+v", got)
	}
	if _, ok := fake.CurrentClaudeSession(conversationID); ok {
		t.Fatal("calling route runtime mapping was not cleared")
	}
}

func TestReleaseClaudeSelectionRejectsCallingRouteActiveWriter(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	taskKey := claudeSessionExecutionKey("session-a")
	task, _, started := h.beginActiveTask(context.Background(), taskKey, activeTaskMeta{
		owner: "user-1", routeUserID: "user-1", agentName: "claude",
	})
	if !started {
		t.Fatal("active task registration failed")
	}
	defer h.finishActiveTask(taskKey, task)

	_, err := h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route: claudeSessionRoute{
			Context: context.Background(), ActorUserID: "user-1", UserID: "user-1",
			AgentName: "claude", Agent: fake, BindingKey: key,
		},
		WorkspaceRoot: workspace, Command: "cd",
	})
	if err == nil || !strings.Contains(err.Error(), "任务仍在执行") {
		t.Fatalf("err=%v", err)
	}
}

func TestReleaseClaudeSelectionAllowsOtherRouteToChangeBindingDuringWriter(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	first := claudeBindingKey("user-1", "claude")
	second := claudeBindingKey("user-2", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[first] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.bindings[second] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	taskKey := claudeSessionExecutionKey("session-a")
	task, _, _ := h.beginActiveTask(context.Background(), taskKey, activeTaskMeta{
		owner: "user-1", routeUserID: "user-1", agentName: "claude",
	})
	defer h.finishActiveTask(taskKey, task)

	_, err := h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route: claudeSessionRoute{
			Context: context.Background(), ActorUserID: "user-2", UserID: "user-2",
			AgentName: "claude", Agent: fake, BindingKey: second,
		},
		WorkspaceRoot: workspace, Command: "cd",
	})
	if err != nil {
		t.Fatalf("other route release: %v", err)
	}
	if got := store.binding(first); got.SessionID != "session-a" {
		t.Fatalf("writer route changed: %+v", got)
	}
}
