package messaging

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestHandleGlobalNewCommitsClaudeACPSession(t *testing.T) {
	h, ag, workspace := newClaudeACPNavigationHandler(t)
	ag.resetSessionID = "session-new"
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(304, "/new"))

	binding := h.ensureClaudeSessions().binding(claudeBindingKey("user-1", "claude"))
	if binding.SessionID != "session-new" || binding.Status != claudeBindingReady {
		t.Fatalf("binding=%+v，期望立即提交新 ACP 会话", binding)
	}
	wantConversationID := buildClaudeConversationID("user-1", "claude", workspace)
	if ag.resetConversationID() != wantConversationID || len(calls.texts()) == 0 {
		t.Fatalf("reset=%q messages=%#v", ag.resetConversationID(), calls.texts())
	}
}

func TestHandleCwdClearsClaudeSessionBinding(t *testing.T) {
	h, _, oldWorkspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	if err := h.ensureClaudeSessions().commitSelection(key, oldWorkspace, "session-old"); err != nil {
		t.Fatal(err)
	}
	newWorkspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{newWorkspace})

	text := h.handleCwd("/cwd "+newWorkspace, "user-1")
	binding := h.ensureClaudeSessions().binding(key)
	if !strings.Contains(text, newWorkspace) || binding.WorkspaceRoot != newWorkspace {
		t.Fatalf("text=%q binding=%+v", text, binding)
	}
	if binding.SessionID != "" || binding.Status != claudeBindingUnbound {
		t.Fatalf("binding=%+v，切换工作空间后不应继承旧会话", binding)
	}
}

func TestClaudeCcLsSortsACPSessionsAcrossWorkspaces(t *testing.T) {
	h, ag, allowedRoot := newClaudeACPNavigationHandler(t)
	workspaceA := allowedRoot + "/alpha"
	workspaceB := allowedRoot + "/beta"
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatal(err)
	}
	h.SetAllowedWorkspaceRoots([]string{allowedRoot})
	ag.catalogSessions = []agent.ClaudeSession{
		{ID: "session-b", Cwd: workspaceB, Title: "Beta", UpdatedAt: "2026-07-13T09:00:00Z"},
		{ID: "session-a", Cwd: workspaceA, Title: "Alpha", UpdatedAt: "2026-07-13T10:00:00Z"},
	}

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc ls")
	if strings.Index(text, "Alpha") > strings.Index(text, "Beta") {
		t.Fatalf("text=%q，期望按 ACP 更新时间排序", text)
	}
	result := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc switch 0")
	if ag.useSessionID != "session-a" || !strings.Contains(result, "已切换") {
		t.Fatalf("session=%q result=%q", ag.useSessionID, result)
	}
}
