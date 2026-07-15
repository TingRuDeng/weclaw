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

func TestCodexCxSwitchUsesCurrentWorkspaceSessionIndex(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "alpha")
	workspaceB := filepath.Join(root, "beta")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "Alpha 会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspaceB, "Beta 会话", "2026-04-29T10:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(114, "/cx cd alpha"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(115, "/cx switch 0"))

	intent := h.codexSessions.controlIntent("thread-a")
	if ag.useThreadID != "" || intent.Owner != codexControlRemote {
		t.Fatalf("use=%q intent=%#v", ag.useThreadID, intent)
	}
	if !containsText(calls.texts(), "已切换并接管") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
	}
	if containsText(calls.texts(), "thread-a") {
		t.Fatalf("switch reply should hide thread id, messages=%#v", calls.texts())
	}
}

func TestCodexCxSwitchDoesNotCreateDraftWhenOtherSessionsExist(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-bad", workspace, "坏历史会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-good", workspace, "可选会话", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	ag.handoffErrors["thread-bad"] = errors.New("探测失败")
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.ensureWorkspace(bindingKey, workspace)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(147, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(148, "/cx switch 0"))

	thread, pending := h.codexSessions.getThread(bindingKey, workspace)
	if thread != "" || pending || h.codexSessions.controlIntent("thread-bad").Owner != codexControlUnclaimed {
		t.Fatalf("探测失败不应提交目标，thread=%q pending=%v", thread, pending)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "切换并接管") || !strings.Contains(text, "失败") ||
		strings.Contains(text, "已进入工作空间并创建新会话草稿") || strings.Contains(text, "thread-store internal error") {
		t.Fatalf("probe failure should preserve selection without a draft, messages=%#v", calls.texts())
	}
}

func TestCodexShortIndexEntersWorkspaceFromWorkspaceList(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	appendLocalCodexTurnContext(t, codexDir, "thread-a", "gpt-5.5", "medium")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(140, "/cx 0"))

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("/cx 0 should enter workspace, got cwd=%q want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	if ag.useThreadID != "" || h.codexSessions.controlIntent("thread-a").Owner != codexControlRemote {
		t.Fatalf("use=%q intent=%#v", ag.useThreadID, h.codexSessions.controlIntent("thread-a"))
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已进入工作空间并接管唯一会话") || strings.Contains(text, "0. 会话 A") {
		t.Fatalf("/cx 0 should auto switch single session, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "模型: gpt-5.5") || !strings.Contains(text, "推理强度: medium") {
		t.Fatalf("auto switch should show session model status, messages=%#v", calls.texts())
	}
}

func TestCodexShortIndexPreservesBindingWhenSingleSessionCannotBeRestored(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	oldWorkspace := filepath.Join(root, "zeta")
	targetWorkspace := filepath.Join(root, "alpha")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-bad", targetWorkspace, "坏历史会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	ag.handoffErrors["thread-bad"] = errors.New("探测失败")
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, oldWorkspace, "thread-old")
	h.codexSessions.setActiveWorkspace(bindingKey, oldWorkspace)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: "user-1", agentName: "codex", bindingKey: bindingKey,
		workspace: oldWorkspace, threadID: "thread-old",
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(146, "/cx 0"))

	active, _ := h.codexSessions.getActiveWorkspace(bindingKey)
	oldThread, _ := h.codexSessions.getThread(bindingKey, oldWorkspace)
	targetThread, pending := h.codexSessions.getThread(bindingKey, targetWorkspace)
	oldIntent := h.codexSessions.controlIntent("thread-old")
	targetIntent := h.codexSessions.controlIntent("thread-bad")
	if active != oldWorkspace || oldThread != "thread-old" || targetThread != "" || pending ||
		oldIntent.Owner != codexControlRemote || targetIntent.Owner != codexControlUnclaimed {
		t.Fatalf("active=%q old=%q target=%q pending=%t intents=(%#v,%#v)", active, oldThread, targetThread, pending, oldIntent, targetIntent)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "切换并接管") || !strings.Contains(text, "失败") ||
		strings.Contains(text, "已进入工作空间并创建新会话草稿") {
		t.Fatalf("reply should preserve user choice, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceWithNoSessionsRequiresExplicitNew(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := filepath.Join(t.TempDir(), "empty")
	oldWorkspace := filepath.Join(t.TempDir(), "old")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("创建测试工作空间失败: %v", err)
	}
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info:           agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
			resetSessionID: "thread-new",
		},
	}
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, oldWorkspace, "thread-old")
	h.codexSessions.setActiveWorkspace(bindingKey, oldWorkspace)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: "user-1", agentName: "codex", bindingKey: bindingKey,
		workspace: oldWorkspace, threadID: "thread-old",
	})
	result := h.enterCodexWorkspace(codexWorkspaceCdRequest{
		Context: context.Background(), UserID: "user-1", ActorUserID: "user-1",
		BindingKey: bindingKey, AgentName: "codex", Agent: ag,
	}, codexWorkspaceGroup{Name: "empty", Root: workspace}, workspace)

	thread, pending := h.codexSessions.getThread(bindingKey, workspace)
	if thread != "" || pending {
		t.Fatalf("thread=%q pending=%v, want unbound workspace", thread, pending)
	}
	active, _ := h.codexSessions.getActiveWorkspace(bindingKey)
	if active != normalizeCodexWorkspaceRoot(workspace) || h.codexSessions.controlIntent("thread-old").Owner != codexControlRemote {
		t.Fatalf("active=%q old intent=%#v", active, h.codexSessions.controlIntent("thread-old"))
	}
	if ag.resetConversationID() != "" {
		t.Fatalf("cd workspace must not create session, reset=%q", ag.resetConversationID())
	}
	if !strings.Contains(result.Reply, "发送 /cx new") || !result.ShowCard {
		t.Fatalf("cd should require explicit new session card, result=%#v", result)
	}
}

func TestCodexShortIndexSwitchesSessionInsideWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(141, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(142, "/cx 0"))

	if ag.useThreadID != "" || h.codexSessions.controlIntent("thread-a").Owner != codexControlRemote {
		t.Fatalf("use=%q intent=%#v", ag.useThreadID, h.codexSessions.controlIntent("thread-a"))
	}
	if !containsText(calls.texts(), "已切换并接管") {
		t.Fatalf("/cx 0 should switch current workspace session, messages=%#v", calls.texts())
	}
}

func TestCodexShortDotDotReturnsToWorkspaceList(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(143, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(144, "/cx .."))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已返回工作空间列表") || !strings.Contains(text, "0. weclaw") {
		t.Fatalf("/cx .. should return to workspace list, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdDotDotReturnsToWorkspaceListWithoutChangingCwd(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(116, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(117, "/cx cd .."))

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("cd .. should not change codex cwd, got %q want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已返回工作空间列表") ||
		!strings.Contains(text, "Codex 工作空间") ||
		!strings.Contains(text, "0. weclaw") {
		t.Fatalf("cd .. reply should include workspace list, messages=%#v", calls.texts())
	}
}

func TestCodexCxPwdShowsBrowseWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(119, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(120, "/cx pwd"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "浏览层级: 会话") || !strings.Contains(text, "工作空间: weclaw") {
		t.Fatalf("pwd should show current browse workspace, messages=%#v", calls.texts())
	}
}
