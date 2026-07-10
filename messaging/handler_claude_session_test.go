package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestHandleGlobalNewResetsActiveClaudeWorkspaceSession(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info:           agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
			resetSessionID: "session-new",
		},
		sessionID: "session-old",
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	bindingKey := claudeBindingKey("user-1", "claude")
	h.claudeSessions.setActiveWorkspace(bindingKey, workspace)
	h.claudeSessions.setSession(bindingKey, workspace, "session-old")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(304, "/new"))

	wantConversationID := buildClaudeConversationID("user-1", "claude", workspace)
	if ag.resetConversationID() != wantConversationID {
		t.Fatalf("reset conversation=%q, want %q", ag.resetConversationID(), wantConversationID)
	}
	sessionID, pending := h.claudeSessions.getSession(bindingKey, workspace)
	if sessionID != "session-new" || pending {
		t.Fatalf("stored session=%q pending=%v, want session-new false", sessionID, pending)
	}
	if !containsText(calls.texts(), "已创建新的claude会话") {
		t.Fatalf("reply should mention new claude session, messages=%#v", calls.texts())
	}
}

func TestHandleCwdRecordsActiveClaudeWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetAllowedWorkspaceRoots([]string{workspace})

	reply := h.handleCwd("/cwd "+workspace, "user-1")

	active, ok := h.claudeSessions.getActiveWorkspace(claudeBindingKey("user-1", "claude"))
	if !ok || active != normalizeClaudeWorkspaceRoot(workspace) {
		t.Fatalf("active workspace=(%q,%v), want %q true; reply=%q", active, ok, normalizeClaudeWorkspaceRoot(workspace), reply)
	}
}

func TestDiscoverLocalClaudeSessionsReadsProjectTranscripts(t *testing.T) {
	claudeDir := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	writeLocalClaudeSession(t, claudeDir, "session-a", workspaceA, "功能 A", "2026-04-28T08:00:00Z")
	writeLocalClaudeSession(t, claudeDir, "session-b", workspaceB, "功能 B", "2026-04-29T08:00:00Z")

	sessions := discoverLocalClaudeSessions(claudeDir)

	if len(sessions) != 2 {
		t.Fatalf("sessions len=%d, want 2: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "session-b" || sessions[0].WorkspaceRoot != normalizeCodexWorkspaceRoot(workspaceB) {
		t.Fatalf("first session=%#v, want newest session-b workspace-b", sessions[0])
	}
	if sessions[1].ThreadName != "功能 A" {
		t.Fatalf("second session name=%q, want 功能 A", sessions[1].ThreadName)
	}
}

func TestDiscoverLocalClaudeSessionsSkipsMissingWorkspace(t *testing.T) {
	claudeDir := t.TempDir()
	existingWorkspace := filepath.Join(t.TempDir(), "existing")
	missingWorkspace := filepath.Join(t.TempDir(), "missing")
	writeLocalClaudeSession(t, claudeDir, "session-existing", existingWorkspace, "现存会话", "2026-04-29T09:00:00Z")
	writeLocalClaudeProjectConfig(t, claudeDir, missingWorkspace)
	writeLocalClaudeTranscript(t, claudeDir, missingWorkspace, "session-missing", "已删除会话", "2026-04-29T10:00:00Z")

	sessions := discoverLocalClaudeSessions(claudeDir)

	if len(sessions) != 1 {
		t.Fatalf("sessions len=%d, want 1: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "session-existing" {
		t.Fatalf("session id=%q, want session-existing", sessions[0].ThreadID)
	}
}

func TestSetClaudeSessionFileRestoresWorkspaceSession(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "claude-sessions.json")
	workspace := t.TempDir()
	bindingKey := claudeBindingKey("user-1", "claude")
	first := NewHandler(nil, nil)
	first.SetClaudeSessionFile(stateFile)
	first.ensureClaudeSessions().setActiveWorkspace(bindingKey, workspace)
	first.ensureClaudeSessions().setSession(bindingKey, workspace, "session-restored")

	second := NewHandler(nil, nil)
	second.SetClaudeSessionFile(stateFile)

	sessionID, pending := second.ensureClaudeSessions().getSession(bindingKey, workspace)
	if sessionID != "session-restored" || pending {
		t.Fatalf("restored session=(%q,%v), want session-restored false", sessionID, pending)
	}
	active, ok := second.ensureClaudeSessions().getActiveWorkspace(bindingKey)
	if !ok || active != normalizeClaudeWorkspaceRoot(workspace) {
		t.Fatalf("restored active workspace=(%q,%v), want %q true", active, ok, normalizeClaudeWorkspaceRoot(workspace))
	}
}

func TestClaudeCcLsIncludesLocalSessionsAndHidesSessionIDs(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "local")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalClaudeSession(t, claudeDir, "session-local", workspace, "本机会话", "2026-04-29T09:00:00Z")
	h.SetClaudeLocalSessionDir(claudeDir)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(301, "/cc ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "Claude 会话") || !strings.Contains(text, "0. local / 本机会话") {
		t.Fatalf("ls should show local session, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "session-local") {
		t.Fatalf("session ls should hide session id, messages=%#v", calls.texts())
	}
}

func TestClaudeModelStatusCommandShowsCurrentConfig(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude", Model: "opus"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc model status")

	if !strings.Contains(text, "Claude 模型配置") || !strings.Contains(text, "model: opus") {
		t.Fatalf("model status reply mismatch: %q", text)
	}
}

func TestClaudeModelLsCommandListsBuiltInModels(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc model ls")

	if !strings.Contains(text, "Claude 可用模型") ||
		!strings.Contains(text, "claude-sonnet-5") ||
		!strings.Contains(text, "alias: sonnet") {
		t.Fatalf("model ls reply mismatch: %q", text)
	}
}

func TestClaudeCcLsNumberMatchesSwitchIndexAcrossSortedWorkspaces(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "aaa")
	workspaceZ := filepath.Join(t.TempDir(), "zzz")
	h.SetAllowedWorkspaceRoots([]string{workspaceA, workspaceZ})
	writeLocalClaudeSession(t, claudeDir, "session-old", workspaceA, "较早会话", "2026-04-28T09:00:00Z")
	writeLocalClaudeSession(t, claudeDir, "session-new", workspaceZ, "较新会话", "2026-04-29T09:00:00Z")
	h.SetClaudeLocalSessionDir(claudeDir)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(303, "/cc ls"))
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "0. zzz / 较新会话") {
		t.Fatalf("ls index 0 should show newest switch target, messages=%#v", calls.texts())
	}

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(304, "/cc switch 0"))

	wantConversationID := buildClaudeConversationID("user-1", "claude", workspaceZ)
	if ag.useConversation != wantConversationID || ag.useSessionID != "session-new" {
		t.Fatalf("use conversation/session=(%q,%q), want (%q,session-new)", ag.useConversation, ag.useSessionID, wantConversationID)
	}
}

func TestHandleClaudeSwitchCommandBindsLocalSessionIndex(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "desktop")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalClaudeSession(t, claudeDir, "session-desktop", workspace, "桌面会话", "2026-04-29T09:00:00Z")
	h.SetClaudeLocalSessionDir(claudeDir)
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(302, "/cc switch 0"))

	wantConversationID := buildClaudeConversationID("user-1", "claude", workspace)
	if ag.useConversation != wantConversationID || ag.useSessionID != "session-desktop" {
		t.Fatalf("use conversation/session=(%q,%q), want (%q,session-desktop)", ag.useConversation, ag.useSessionID, wantConversationID)
	}
	if ag.conversationCwds[wantConversationID] != normalizeClaudeWorkspaceRoot(workspace) {
		t.Fatalf("conversation cwd=%q, want %q", ag.conversationCwds[wantConversationID], normalizeClaudeWorkspaceRoot(workspace))
	}
	if !containsText(calls.texts(), "已切换 Claude 会话") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
	}
}
