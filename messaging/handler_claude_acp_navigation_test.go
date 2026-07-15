package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestClaudeCcLsUsesACPCatalogOnly(t *testing.T) {
	h, fake, allowed := newClaudeACPNavigationHandler(t)
	blocked := t.TempDir()
	fake.catalogSessions = []agent.ClaudeSession{
		{ID: "session-allowed", Cwd: allowed, Title: "允许会话", UpdatedAt: "2026-07-13T10:00:00Z"},
		{ID: "session-blocked", Cwd: blocked, Title: "越权会话", UpdatedAt: "2026-07-13T11:00:00Z"},
	}

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc ls")
	if !strings.Contains(text, "允许会话") || strings.Contains(text, "越权会话") || fake.listCalls != 1 {
		t.Fatalf("text=%q listCalls=%d", text, fake.listCalls)
	}
}

func TestClaudeSwitchCommitsACPBindingAndShowsConfig(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-1", Cwd: workspace, Title: "目标会话"}}
	fake.sessionConfig = agent.ClaudeSessionConfig{Model: "opus", Effort: "high"}

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc switch 0")
	binding := h.ensureClaudeSessions().binding(claudeBindingKey("user-1", "claude"))
	if fake.useSessionID != "session-1" || binding.SessionID != "session-1" || binding.Status != claudeBindingReady {
		t.Fatalf("use=%q binding=%+v", fake.useSessionID, binding)
	}
	if !strings.Contains(text, "模型: opus") || !strings.Contains(text, "推理强度: high") {
		t.Fatalf("text=%q, want current session config", text)
	}
	if !strings.Contains(text, "恢复状态: 已就绪") {
		t.Fatalf("text=%q, want ready status", text)
	}
}

func TestClaudeNewCreatesAndOwnsImmediately(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	fake.resetSessionID = "session-new"

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
	binding := h.ensureClaudeSessions().binding(claudeBindingKey("user-1", "claude"))
	if fake.resetConversationID() == "" || binding.WorkspaceRoot != workspace || binding.SessionID != "session-new" || binding.Status != claudeBindingReady {
		t.Fatalf("reset=%q binding=%+v", fake.resetConversationID(), binding)
	}
	intent := h.ensureClaudeSessions().controlIntent("session-new")
	if intent.Owner != claudeOwnerRemote || intent.BindingKey != claudeBindingKey("user-1", "claude") {
		t.Fatalf("intent=%+v", intent)
	}
	if !strings.Contains(text, "已创建并接管 Claude 会话") {
		t.Fatalf("text=%q", text)
	}
}

func TestClaudeNormalMessageRequiresExplicitBinding(t *testing.T) {
	h, fake, _ := newClaudeACPNavigationHandler(t)
	_, err := h.resolveAgentConversationIDForRoute(context.Background(), "user-1", "user-1", "claude", fake)
	if err == nil || !strings.Contains(err.Error(), "/cc ls") || !strings.Contains(err.Error(), "/cc new") {
		t.Fatalf("error=%v, want explicit selection prompt", err)
	}
}

func TestClaudeResumeFailureRetainsBinding(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-1", Cwd: workspace}}
	seedClaudeRemoteControl(t, h, "user-1", "claude", workspace, "session-1", 1)
	h.ensureClaudeSessions().markPendingResume(claudeBindingKey("user-1", "claude"))
	fake.useErr = errors.New("resume failed")

	_, err := h.resolveAgentConversationIDForRoute(context.Background(), "user-1", "user-1", "claude", fake)
	binding := h.ensureClaudeSessions().binding(claudeBindingKey("user-1", "claude"))
	if err == nil || binding.SessionID != "session-1" || binding.Status != claudeBindingResumeFailed {
		t.Fatalf("error=%v binding=%+v", err, binding)
	}
}

func TestClaudePendingResumeBecomesReady(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	seedClaudeRemoteControl(t, h, "user-1", "claude", workspace, "session-1", 1)
	if err := h.ensureClaudeSessions().markPendingResume(key); err != nil {
		t.Fatal(err)
	}

	conversationID, err := h.resolveAgentConversationIDForRoute(context.Background(), "user-1", "user-1", "claude", fake)
	if err != nil {
		t.Fatal(err)
	}
	binding := h.ensureClaudeSessions().binding(key)
	if binding.Status != claudeBindingReady || fake.useSessionID != "session-1" || conversationID == "" {
		t.Fatalf("binding=%+v use=%q conversation=%q", binding, fake.useSessionID, conversationID)
	}
}

func TestClaudeSwitchSaveFailureRollsBackRuntime(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	if err := h.ensureClaudeSessions().commitSelection(key, workspace, "session-old"); err != nil {
		t.Fatal(err)
	}
	fake.sessionID = "session-old"
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-new", Cwd: workspace}}
	h.ensureClaudeSessions().persist = func(claudeSessionState) error { return errors.New("disk full") }

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc switch 0")
	binding := h.ensureClaudeSessions().binding(key)
	if !strings.Contains(text, "失败，请稍后重试") || strings.Contains(text, "disk full") || binding.SessionID != "session-old" {
		t.Fatalf("text=%q binding=%+v", text, binding)
	}
	if len(fake.useCalls) != 2 || fake.useCalls[0] != "session-new" || fake.useCalls[1] != "session-old" {
		t.Fatalf("useCalls=%#v，期望恢复旧 ACP runtime", fake.useCalls)
	}
}

func TestClaudeNewFailureRestoresPreviousRuntime(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	if err := h.ensureClaudeSessions().commitSelection(key, workspace, "session-old"); err != nil {
		t.Fatal(err)
	}
	fake.sessionID = "session-old"
	fake.resetClears = true
	fake.resetErr = errors.New("session/new failed: /Users/private/claude-runtime.json")

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
	binding := h.ensureClaudeSessions().binding(key)
	if !strings.Contains(text, "新建 Claude 会话失败") || strings.Contains(text, "/Users/private") || binding.SessionID != "session-old" {
		t.Fatalf("text=%q binding=%+v", text, binding)
	}
	if fake.useSessionID != "session-old" || fake.sessionID != "session-old" {
		t.Fatalf("use=%q runtime=%q，期望恢复旧 session", fake.useSessionID, fake.sessionID)
	}
}

func TestClaudeSwitchAgentSelectionSaveFailureRollsBack(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	if err := h.ensureClaudeSessions().commitSelection(key, workspace, "session-old"); err != nil {
		t.Fatal(err)
	}
	fake.sessionID = "session-old"
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-new", Cwd: workspace}}
	statePath := filepath.Join(t.TempDir(), "agent-sessions.json")
	if err := h.SetAgentSessionFile(statePath); err != nil {
		t.Fatal(err)
	}
	if err := h.ensureAgentSessions().Set("user-1", "codex"); err != nil {
		t.Fatal(err)
	}
	invalidParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.ensureAgentSessions().filePath = filepath.Join(invalidParent, "state.json")

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc switch 0")
	binding := h.ensureClaudeSessions().binding(key)
	selected, _ := h.ensureAgentSessions().Get("user-1")
	if !strings.Contains(text, "失败") || binding.SessionID != "session-old" || selected != "codex" {
		t.Fatalf("text=%q binding=%+v selected=%q", text, binding, selected)
	}
	if fake.useSessionID != "session-old" {
		t.Fatalf("runtime=%q，期望恢复旧 session", fake.useSessionID)
	}
}

func TestClaudeStatusShowsSessionConfigAndRecoveryState(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	if err := h.ensureClaudeSessions().commitSelection(key, workspace, "session-1"); err != nil {
		t.Fatal(err)
	}
	fake.sessionConfig = agent.ClaudeSessionConfig{Model: "opus", Effort: "high"}

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc status")
	for _, want := range []string{"session-1", "恢复状态: 已就绪", "模型: opus", "推理强度: high"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text=%q，缺少 %q", text, want)
		}
	}
}

func newClaudeACPNavigationHandler(t *testing.T) (*Handler, *fakeClaudeSessionAgent, string) {
	t.Helper()
	workspace := t.TempDir()
	fake := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}}}
	h := NewHandler(nil, nil)
	h.defaultName = "claude"
	h.agents["claude"] = fake
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	return h, fake, workspace
}
