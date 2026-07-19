package messaging

import (
	"context"
	"strings"
	"testing"
)

func TestClaudeOwnerAndCLICommandsAreDisabled(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetDefaultAgent("claude", &fakeClaudeSessionAgent{})
	for _, command := range []string{"/cc owner", "/cc owner remote", "/cc owner local", "/cc cli"} {
		text := h.handleClaudeSessionCommand(context.Background(), "user-1", command)
		if !strings.Contains(text, "已停用") || !strings.Contains(text, "单一共享 ClaudeHost") {
			t.Fatalf("command=%q reply=%q", command, text)
		}
	}
}

func TestClaudeWritableBindingUsesOnlyFrontendBinding(t *testing.T) {
	store := newClaudeSessionStore()
	first := claudeBindingKey("route-a", "claude")
	second := claudeBindingKey("route-b", "claude")
	store.bindings[first] = newClaudeBinding("/workspace", "session-shared", claudeBindingReady)
	store.bindings[second] = newClaudeBinding("/workspace", "session-shared", claudeBindingReady)

	for _, key := range []string{first, second} {
		binding, err := store.requireWritableBinding(key)
		if err != nil || binding.SessionID != "session-shared" {
			t.Fatalf("key=%q binding=%+v err=%v", key, binding, err)
		}
	}
}

func TestClaudeStatusReportsSessionWriterRoute(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeClaudeSessionAgent{}
	workspace := t.TempDir()
	route := newClaudeAcquireRoute(context.Background(), "route-a", "route-a", "claude", ag, workspace)
	seedClaudeBinding(t, h, route.UserID, route.AgentName, workspace, "session-shared", 1)

	taskKey := claudeSessionExecutionKey("session-shared")
	task, _, started := h.beginActiveTask(context.Background(), taskKey, activeTaskMeta{
		owner: "route-b", routeUserID: "route-b", agentName: "claude",
	})
	if !started {
		t.Fatal("active writer registration failed")
	}
	defer h.finishActiveTask(taskKey, task)

	if status := h.renderClaudeStatus(route); !strings.Contains(status, "writer: 其他窗口执行中") {
		t.Fatalf("status=%q", status)
	}
}

func seedClaudeBinding(t *testing.T, h *Handler, routeUserID, agentName, workspace, sessionID string, revision uint64) string {
	t.Helper()
	key := claudeBindingKey(routeUserID, agentName)
	binding := newClaudeBinding(workspace, sessionID, claudeBindingReady)
	if revision > 0 {
		binding.Revision = revision
	}
	h.ensureClaudeSessions().bindings[key] = binding
	return key
}
