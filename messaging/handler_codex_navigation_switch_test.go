package messaging

import (
	"context"
	"fmt"
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
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(114, "/cx cd alpha"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(115, "/cx switch 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspaceA)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-a" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-a)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if !containsText(calls.texts(), "已切换会话") {
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
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		useErr: fmt.Errorf("thread-store internal error: failed to read thread"),
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.ensureWorkspace(bindingKey, workspace)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(147, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(148, "/cx switch 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-bad" {
		t.Fatalf("应尝试切换目标 thread，use conversation/thread=(%q,%q)", ag.useConversation, ag.useThreadID)
	}
	if ag.clearCalledWith != "" {
		t.Fatalf("有其他可选会话时不应清理当前会话，clear=%q", ag.clearCalledWith)
	}
	thread, pending := h.codexSessions.getThread(bindingKey, workspace)
	if thread != "" || pending {
		t.Fatalf("有其他可选会话时不应创建新会话草稿，thread=%q pending=%v", thread, pending)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "切换会话失败") || strings.Contains(text, "已进入工作空间并创建新会话草稿") {
		t.Fatalf("switch failure should not create draft, messages=%#v", calls.texts())
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
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(140, "/cx 0"))

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("/cx 0 should enter workspace, got cwd=%q want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-a" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-a)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已进入工作空间并切换会话") || strings.Contains(text, "0. 会话 A") {
		t.Fatalf("/cx 0 should auto switch single session, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "模型: gpt-5.5") || !strings.Contains(text, "推理强度: medium") {
		t.Fatalf("auto switch should show session model status, messages=%#v", calls.texts())
	}
}

func TestCodexShortIndexPreservesBindingWhenSingleSessionCannotBeRestored(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-bad", workspace, "坏历史会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		useErr: fmt.Errorf("thread-store internal error: failed to read thread"),
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-bad")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(146, "/cx 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-bad" {
		t.Fatalf("应先尝试恢复唯一 thread，use conversation/thread=(%q,%q)", ag.useConversation, ag.useThreadID)
	}
	if ag.clearCalledWith != "" {
		t.Fatalf("坏 thread 不应清理原绑定，clear=%q", ag.clearCalledWith)
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-bad" || pending {
		t.Fatalf("坏 thread 应保留原绑定，thread=%q pending=%v", thread, pending)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "原会话无法被微信接手") || !strings.Contains(text, "/cx new") ||
		strings.Contains(text, "已进入工作空间并创建新会话草稿") {
		t.Fatalf("reply should preserve user choice, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceWithNoSessionsRequiresExplicitNew(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := filepath.Join(t.TempDir(), "empty")
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
	text := h.enterCodexWorkspaceWithoutSessionsResult(codexWorkspaceCdRequest{
		Context: context.Background(), UserID: "user-1", ActorUserID: "user-1",
		BindingKey: bindingKey, AgentName: "codex", Agent: ag,
	}, "empty", workspace).Reply

	thread, pending := h.codexSessions.getThread(bindingKey, workspace)
	if thread != "" || pending {
		t.Fatalf("thread=%q pending=%v, want unbound workspace", thread, pending)
	}
	if ag.resetConversationID() != "" {
		t.Fatalf("cd workspace must not create session, reset=%q", ag.resetConversationID())
	}
	if !strings.Contains(text, "发送 /cx new") {
		t.Fatalf("cd should require explicit new session, text=%q", text)
	}
}

func TestCodexShortIndexSwitchesSessionInsideWorkspace(t *testing.T) {
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

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(141, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(142, "/cx 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-a" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-a)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if !containsText(calls.texts(), "已切换会话") {
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
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
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
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
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
