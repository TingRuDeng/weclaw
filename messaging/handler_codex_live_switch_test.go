package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexSwitchDesktopActiveSkipsUseCodexThread(t *testing.T) {
	state := agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		Preview: "正在审查", Model: "gpt-live", Effort: "high",
	}
	h, ag, workspace := codexLiveSwitchFixture(t, state)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	text := h.handleCodexSwitchForRouteWithOptions(
		context.Background(), "user-1", "codex", workspace, ag, "thread-1", "",
		codexSwitchOptions{actorUserID: "user-1", platform: platform.PlatformFeishu, reply: reply},
	)
	if ag.useThreadID != "" || !strings.Contains(text, "任务正在进行") {
		t.Fatalf("use=%q text=%q", ag.useThreadID, text)
	}
}

func TestCodexSwitchDesktopIdleSkipsUseCodexThread(t *testing.T) {
	state := agent.CodexThreadState{ThreadID: "thread-1", Model: "gpt-live", Effort: "high"}
	h, ag, workspace := codexLiveSwitchFixture(t, state)
	text := h.handleCodexSwitchForRouteWithOptions(
		context.Background(), "user-1", "codex", workspace, ag, "thread-1", "", codexSwitchOptions{actorUserID: "user-1"},
	)
	if ag.useThreadID != "" || !strings.Contains(text, "模型: gpt-live") || !strings.Contains(text, "推理强度: high") {
		t.Fatalf("use=%q text=%q", ag.useThreadID, text)
	}
}

func TestCodexSingleSessionAutoSwitchDesktopOwnerSkipsUseCodexThread(t *testing.T) {
	state := agent.CodexThreadState{ThreadID: "thread-1", Model: "gpt-live", Effort: "medium"}
	h, ag, workspace := codexLiveSwitchFixture(t, state)
	text := h.enterCodexWorkspaceWithSingleSession(codexWorkspaceCdRequest{
		Context: context.Background(), UserID: "user-1", ActorUserID: "user-1",
		BindingKey: codexBindingKey("user-1", "codex"), AgentName: "codex", Agent: ag,
	}, "workspace", workspace, codexWorkspaceView{WorkspaceRoot: workspace, ThreadID: "thread-1"})
	if ag.useThreadID != "" || !strings.Contains(text, "模型: gpt-live") {
		t.Fatalf("use=%q text=%q", ag.useThreadID, text)
	}
}

func TestCodexSwitchUnknownOwnerDoesNotResumeACP(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	ag.binding.Owner = agent.CodexOwnerUnknown
	ag.bindErr = agent.ErrCodexDesktopOwnershipUnknown
	text := h.handleCodexSwitchForRouteWithOptions(
		context.Background(), "user-1", "codex", workspace, ag, "thread-1", "", codexSwitchOptions{actorUserID: "user-1"},
	)
	if ag.useThreadID != "" || ag.recoverCalls != 0 || !strings.Contains(text, "所有权未知") {
		t.Fatalf("use=%q recover=%d text=%q", ag.useThreadID, ag.recoverCalls, text)
	}
}

// TestCodexExplicitSwitchRecoversDisconnectedIdleThread 验证用户选择会话即授权接管空闲断线 thread。
func TestCodexExplicitSwitchRecoversDisconnectedIdleThread(t *testing.T) {
	h, ag, route := disconnectedCodexResolutionFixture(t, false)
	text := h.handleCodexSwitchForRouteWithOptions(
		context.Background(), "user-1", "codex", route.workspaceRoot, ag, "thread-1", "",
		codexSwitchOptions{actorUserID: "user-1"},
	)
	if ag.recoverCalls != 1 || ag.binding.Owner != agent.CodexOwnerWeClawRuntime || !strings.Contains(text, "已切换会话") {
		t.Fatalf("recover=%d binding=%#v text=%q", ag.recoverCalls, ag.binding, text)
	}
}

// TestCodexNormalPrepareDoesNotRecoverDisconnectedThread 验证普通消息不能把断线状态当成接管授权。
func TestCodexNormalPrepareDoesNotRecoverDisconnectedThread(t *testing.T) {
	h, ag, route := disconnectedCodexResolutionFixture(t, false)

	err := h.prepareCodexConversation(context.Background(), route, ag)

	if !errors.Is(err, agent.ErrCodexDesktopDisconnected) || ag.recoverCalls != 0 {
		t.Fatalf("err=%v recover=%d, want disconnected without recovery", err, ag.recoverCalls)
	}
}

// TestCodexExplicitSwitchDoesNotRecoverDisconnectedActiveThread 验证活动 rollout 不能被显式切换抢占。
func TestCodexExplicitSwitchDoesNotRecoverDisconnectedActiveThread(t *testing.T) {
	h, ag, route := disconnectedCodexResolutionFixture(t, true)
	h.handleCodexSwitchForRouteWithOptions(
		context.Background(), "user-1", "codex", route.workspaceRoot, ag, "thread-1", "",
		codexSwitchOptions{actorUserID: "user-1"},
	)
	if ag.recoverCalls != 0 {
		t.Fatalf("recover=%d, want 0 for active rollout", ag.recoverCalls)
	}
}

func TestCodexSwitchBlocksDifferentThreadWhileTaskRuns(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-new"})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-old")
	conversationID := buildCodexConversationID("user-1", "codex", workspace)
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{owner: "user-1"})
	if !started {
		t.Fatal("未能创建测试任务")
	}
	defer h.finishActiveTask(conversationID, task)
	text := h.handleCodexSwitchForRouteWithOptions(
		context.Background(), "user-1", "codex", workspace, ag, "thread-new", "", codexSwitchOptions{actorUserID: "user-1"},
	)
	if !strings.Contains(text, "任务执行期间不能切换") || ag.bindCalls != 0 {
		t.Fatalf("bind=%d text=%q", ag.bindCalls, text)
	}
}

func TestCodexPrepareConversationRechecksOwnerEveryTurn(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	route := h.codexConversationRouteForSession("user-1", "user-1", "codex", ag)
	if err := h.prepareCodexConversation(context.Background(), route, ag); err != nil {
		t.Fatal(err)
	}
	if err := h.prepareCodexConversation(context.Background(), route, ag); err != nil {
		t.Fatal(err)
	}
	if route.threadID != "thread-1" || ag.bindCalls != 2 {
		t.Fatalf("route=%#v bind=%d workspace=%s", route, ag.bindCalls, workspace)
	}
}

func TestCodexBroadcastUsesOwnerAwareBinding(t *testing.T) {
	h, ag, _ := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	route := h.codexConversationRouteForSession("user-1", "user-1", "codex", ag)
	conversationID, err := h.broadcastConversationID(context.Background(), broadcastAgentsRequest{
		userID: "user-1", routeUserID: "user-1",
	}, "codex", ag, route)
	if err != nil || conversationID != route.conversationID || ag.bindCalls != 1 || ag.useThreadID != "" {
		t.Fatalf("conversation=%q err=%v bind=%d use=%q", conversationID, err, ag.bindCalls, ag.useThreadID)
	}
}

func TestCodexPersistedOwnerWaitsForActiveRollout(t *testing.T) {
	h, ag, route := persistedCodexResolutionFixture(t, true)
	resolution, err := h.resolveCodexRuntime(context.Background(), codexRuntimeResolveOptions{
		route: route, threadID: "thread-1", ag: ag,
	})
	if err != nil || ag.recoverCalls != 0 || !resolution.Rollout.Active {
		t.Fatalf("resolution=%#v recover=%d err=%v", resolution, ag.recoverCalls, err)
	}
}

func TestCodexPersistedOwnerRecoversAfterTerminalRollout(t *testing.T) {
	h, ag, route := persistedCodexResolutionFixture(t, false)
	resolution, err := h.resolveCodexRuntime(context.Background(), codexRuntimeResolveOptions{
		route: route, threadID: "thread-1", ag: ag,
	})
	if err != nil || ag.recoverCalls != 1 || resolution.Binding.Owner != agent.CodexOwnerWeClawRuntime {
		t.Fatalf("resolution=%#v recover=%d err=%v", resolution, ag.recoverCalls, err)
	}
}

func codexLiveSwitchFixture(t *testing.T, state agent.CodexThreadState) (*Handler, *fakeCodexLiveAgent, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexLiveAgent(agent.CodexOwnerDesktopLive, state)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-1")
	return h, ag, workspace
}

func persistedCodexResolutionFixture(t *testing.T, active bool) (*Handler, *fakeCodexLiveAgent, codexConversationRoute) {
	t.Helper()
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	ag.binding.Owner = agent.CodexOwnerPersistedOnly
	codexDir := t.TempDir()
	writeLocalCodexSession(t, codexDir, "thread-1", workspace, "会话", "2026-07-11T09:00:00Z")
	meta := readLocalCodexSessionMetas(codexDir + "/sessions")["thread-1"]
	appendCodexRolloutRecord(t, meta.Path, rolloutTaskStartedRecord("turn-1"))
	if !active {
		appendCodexRolloutRecord(t, meta.Path, rolloutTaskCompleteRecord("turn-1", "完成"))
	}
	h.SetCodexLocalSessionDir(codexDir)
	return h, ag, h.codexConversationRouteForSession("user-1", "user-1", "codex", ag)
}

func disconnectedCodexResolutionFixture(t *testing.T, active bool) (*Handler, *fakeCodexLiveAgent, codexConversationRoute) {
	h, ag, route := persistedCodexResolutionFixture(t, active)
	ag.binding.Owner = agent.CodexOwnerDesktopDisconnected
	ag.binding.Connected = false
	ag.bindErr = agent.ErrCodexDesktopDisconnected
	return h, ag, route
}
