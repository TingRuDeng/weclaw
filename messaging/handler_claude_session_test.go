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
	canonicalWorkspace := canonicalTestPath(t, newWorkspace)
	if !strings.Contains(text, canonicalWorkspace) || binding.WorkspaceRoot != canonicalWorkspace {
		t.Fatalf("text=%q binding=%+v", text, binding)
	}
	if binding.SessionID != "" || binding.Status != claudeBindingUnbound {
		t.Fatalf("binding=%+v，切换工作空间后不应继承旧会话", binding)
	}
}

func TestClaudeCdReleasesSelectedRemoteSession(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1,
	}
	fake.runtimeSessions = map[string]string{conversationID: "session-a"}
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	h.SetAllowedWorkspaceRoots([]string{workspace, other})
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-b", Cwd: other}}

	result := h.handleClaudeSessionCommandForRouteResult(
		context.Background(), "user-1", "user-1", true, "/cc cd "+shortCodexWorkspaceName(other),
	)
	if result.Reply == "" || store.binding(key).SessionID != "" {
		t.Fatalf("result=%+v binding=%+v", result, store.binding(key))
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal {
		t.Fatalf("intent=%+v", got)
	}
	if _, ok := fake.CurrentClaudeSession(conversationID); ok {
		t.Fatalf("runtime=%+v", fake.runtimeSessions)
	}
}

func TestHandleCwdReleasesClaudeOwnerBeforeChangingRuntimeCwd(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1,
	}
	fake.runtimeSessions = map[string]string{conversationID: "session-a"}
	newWorkspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{newWorkspace})

	text := h.handleCwd("/cwd "+newWorkspace, "user-1")
	if !strings.Contains(text, canonicalTestPath(t, newWorkspace)) {
		t.Fatalf("text=%q", text)
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal {
		t.Fatalf("intent=%+v", got)
	}
	if got := fake.lastWorkingDir(); got != canonicalTestPath(t, newWorkspace) {
		t.Fatalf("cwd=%q", got)
	}
}

func TestHandleCwdDoesNotChangeRuntimeCwdWhenClaudeReleaseFails(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1,
	}
	store.persist = func(claudeSessionState) error {
		return errors.New("open /Users/private/claude-sessions.json: permission denied")
	}
	newWorkspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{newWorkspace})

	text := h.handleCwd("/cwd "+newWorkspace, "user-1")
	if !strings.Contains(text, "切换 Claude 工作空间失败") || strings.Contains(text, "/Users/private") {
		t.Fatalf("text=%q", text)
	}
	if got := fake.lastWorkingDir(); got != "" {
		t.Fatalf("release 失败后不应更新 runtime cwd: %q", got)
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerRemote {
		t.Fatalf("intent=%+v", got)
	}
}

func TestHandleCwdMultipleClaudeAgentsKeepsCwdWhenLaterReleaseFails(t *testing.T) {
	h, first, workspace := newClaudeACPNavigationHandler(t)
	second := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "@agentclientprotocol/claude-agent-acp", Type: "acp", Command: "claude-agent-acp",
	}}}
	h.agents["claude-2"] = second
	h.SetAgentWorkDirs(map[string]string{"claude": workspace, "claude-2": workspace})
	store := h.ensureClaudeSessions()
	for _, name := range []string{"claude", "claude-2"} {
		key := claudeBindingKey("user-1", name)
		sessionID := "session-" + name
		conversationID := buildClaudeConversationID("user-1", name, workspace)
		store.bindings[key] = newClaudeBinding(workspace, sessionID, claudeBindingReady)
		store.controls[sessionID] = claudeControlIntent{
			Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1,
		}
	}
	persistCalls := 0
	store.persist = func(claudeSessionState) error {
		persistCalls++
		if persistCalls == 2 {
			return os.ErrPermission
		}
		return nil
	}
	newWorkspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{newWorkspace})

	text := h.handleCwd("/cwd "+newWorkspace, "user-1")
	if !strings.Contains(text, "切换 Claude 工作空间失败") {
		t.Fatalf("text=%q", text)
	}
	if first.lastWorkingDir() != "" || second.lastWorkingDir() != "" {
		t.Fatalf("first cwd=%q second cwd=%q", first.lastWorkingDir(), second.lastWorkingDir())
	}
	if got := store.controlIntent("session-claude"); got.Owner != claudeOwnerLocal {
		t.Fatalf("first intent=%+v", got)
	}
	if got := store.controlIntent("session-claude-2"); got.Owner != claudeOwnerRemote {
		t.Fatalf("second intent=%+v", got)
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

func TestClaudeBindingChangeRejectedWhileTaskRuns(t *testing.T) {
	for _, command := range []string{"/cc switch 0", "/cc new"} {
		t.Run(command, func(t *testing.T) {
			h, ag, workspace := newClaudeACPNavigationHandler(t)
			key := claudeBindingKey("user-1", "claude")
			if err := h.claudeSessions.commitSelection(key, workspace, "session-old"); err != nil {
				t.Fatal(err)
			}
			ag.catalogSessions = []agent.ClaudeSession{{ID: "session-next", Cwd: workspace}}
			ag.resetSessionID = "session-next"
			executionKey := buildClaudeConversationID("user-1", "claude", workspace)
			task, _, started := h.beginActiveTask(context.Background(), executionKey, activeTaskMeta{owner: "user-1"})
			if !started {
				t.Fatal("活动任务登记失败")
			}
			defer h.finishActiveTask(executionKey, task)

			text := h.handleClaudeSessionCommand(context.Background(), "user-1", command)
			binding := h.claudeSessions.binding(key)
			if !strings.Contains(text, "任务正在运行") || binding.SessionID != "session-old" {
				t.Fatalf("command=%q text=%q binding=%+v", command, text, binding)
			}
		})
	}
}

func TestClaudeIdentityDoesNotInferFromCommand(t *testing.T) {
	if isClaudeAgent("generic", agent.AgentInfo{Name: "generic", Command: "/tmp/claude-wrapper"}) {
		t.Fatal("不得从 command 文件名推断 Claude 身份")
	}
	if !isClaudeAgent("claude", agent.AgentInfo{Name: "generic"}) {
		t.Fatal("配置 Agent 名称 claude 应识别为 Claude")
	}
	if !isClaudeAgent("generic", agent.AgentInfo{Name: "@agentclientprotocol/claude-agent-acp"}) {
		t.Fatal("官方 ACP agentInfo 名称应识别为 Claude")
	}
}
