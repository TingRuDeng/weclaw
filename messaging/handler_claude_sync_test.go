package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestClaudeCcLsClearsStoredSessionMissingFromLocalWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "desktop")
	writeLocalClaudeSession(t, claudeDir, "session-live", workspace, "本机会话", "2026-04-29T09:00:00Z")
	h.SetClaudeLocalSessionDir(claudeDir)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	bindingKey := claudeBindingKey("user-1", "claude")
	h.claudeSessions.setSession(bindingKey, workspace, "session-deleted")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(305, "/cc ls"))

	text := strings.Join(calls.texts(), "\n")
	if strings.Contains(text, "未命名会话") {
		t.Fatalf("ls should hide stored stale session, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "0. desktop / 本机会话") {
		t.Fatalf("ls should keep visible local session, messages=%#v", calls.texts())
	}
	sessionID, pending := h.claudeSessions.getSession(bindingKey, workspace)
	if sessionID != "" || pending {
		t.Fatalf("stored stale session=(%q,%v), want empty false", sessionID, pending)
	}
}

func TestClaudeCcLsClearsStoredSessionWhenLocalWorkspaceHasNoSessions(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "empty")
	writeLocalClaudeProjectConfig(t, claudeDir, workspace)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	h.SetClaudeLocalSessionDir(claudeDir)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	bindingKey := claudeBindingKey("user-1", "claude")
	h.claudeSessions.setSession(bindingKey, workspace, "session-deleted")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(307, "/cc ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "当前还没有可切换的 Claude 会话") {
		t.Fatalf("ls should report no switchable sessions, messages=%#v", calls.texts())
	}
	sessionID, pending := h.claudeSessions.getSession(bindingKey, workspace)
	if sessionID != "" || pending {
		t.Fatalf("stored stale session=(%q,%v), want empty false", sessionID, pending)
	}
}

func TestClaudeSwitchIndexSkipsStoredSessionMissingFromLocalWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "desktop")
	writeLocalClaudeSession(t, claudeDir, "session-live", workspace, "本机会话", "2026-04-29T09:00:00Z")
	h.SetClaudeLocalSessionDir(claudeDir)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	bindingKey := claudeBindingKey("user-1", "claude")
	h.claudeSessions.setSession(bindingKey, workspace, "session-deleted")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(306, "/cc switch 0"))

	wantConversationID := buildClaudeConversationID("user-1", "claude", workspace)
	if ag.useConversation != wantConversationID || ag.useSessionID != "session-live" {
		t.Fatalf("use conversation/session=(%q,%q), want (%q,session-live); messages=%#v", ag.useConversation, ag.useSessionID, wantConversationID, calls.texts())
	}
}
