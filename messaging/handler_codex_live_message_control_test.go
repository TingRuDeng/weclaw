package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexDesktopIdleMessageStartsDesktopTurn(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
	if ag.bindCalls == 0 || ag.lastChatMessage() != "继续任务" {
		t.Fatalf("bind=%d message=%q", ag.bindCalls, ag.lastChatMessage())
	}
}

func TestCodexDesktopActiveMessageQueuesOnePending(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, true)
	ag.watchDone = make(chan struct{})
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool {
		task, ok := h.activeTask(route.conversationID)
		return ok && task.pendingGuide() == "继续任务"
	})
	task, _ := h.activeTask(route.conversationID)
	defer task.cancel()
	if ag.chatCallCount() != 0 {
		t.Fatal("active Desktop thread 不应开始新 turn")
	}
}

func TestCodexDesktopQueuedMessageKeepsResolvedStreamProgress(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, true)
	ag.watchDone = make(chan struct{})
	ag.watchProgress = "正在处理本地任务"
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Typing: true, Streaming: true})
	opts.reply = reply
	opts.progressCfg = config.DefaultProgressConfig()
	opts.progressCfg.Mode = progressModeStream
	opts.progressCfg.InitialDelaySeconds = 0

	h.startCodexAgentTask(opts)
	select {
	case <-reply.StreamOpened:
	case <-time.After(taskWaitTimeout):
		t.Fatalf("typing=%#v，排队外部任务未继承 stream 配置", reply.TypingStates)
	}
	task, ok := h.activeTask(route.conversationID)
	if !ok {
		t.Fatal("排队后外部任务未登记")
	}
	defer task.cancel()
	if reply.Stream.Options.Title == "" {
		t.Fatalf("typing=%#v，排队外部任务未继承 stream 配置", reply.TypingStates)
	}
	if len(reply.TypingStates) != 0 {
		t.Fatalf("typing=%#v，stream 模式不应创建 typing 卡", reply.TypingStates)
	}
}

func TestCodexDesktopDisconnectedRejectsStartGuideAndStop(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)
	ag.bindErr = agent.ErrCodexDesktopDisconnected
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return len(opts.reply.(*platformtest.Replier).Texts) > 0 })
	text := strings.Join(opts.reply.(*platformtest.Replier).Texts, "\n")
	if ag.chatCallCount() != 0 || !strings.Contains(text, "连接已断开") {
		t.Fatalf("chat=%d text=%q", ag.chatCallCount(), text)
	}
	if _, active := h.activeTask(route.conversationID); active {
		t.Fatal("断线拒绝后不应留下 active task")
	}
}

func TestCodexDesktopGuideUsesCurrentTurn(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	h.agents["codex"] = ag
	h.defaultName = "codex"
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexRuntimeDesktop,
		codexThreadID: "thread-1", codexTurnID: "turn-old",
	})
	h.storePendingGuide(route.conversationID, pendingAgentTask{message: "补充要求", run: func() {}})
	text, handled := h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{ctx: context.Background(), key: route.conversationID, agentName: "codex", actor: "user-1"})
	if !handled || !strings.Contains(text, "已发送") || ag.steerTurnID != "turn-1" {
		t.Fatalf("handled=%v text=%q turn=%q", handled, text, ag.steerTurnID)
	}
	_ = task
}

func TestCodexDesktopStopWaitsForTerminalAndKeepsPending(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexRuntimeDesktop,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	h.storePendingGuide(route.conversationID, pendingAgentTask{message: "下一条", run: func() {}})
	text, handled := h.interruptExternalCodexTask(externalCodexTaskCommand{ctx: context.Background(), key: route.conversationID, agent: ag, actor: "user-1"})
	if !handled || !strings.Contains(text, "等待任务终态") {
		t.Fatalf("handled=%v text=%q", handled, text)
	}
	if taskPhase(task) != codexTaskStopping || task.pendingGuide() != "下一条" {
		t.Fatalf("phase=%s pending=%q", taskPhase(task), task.pendingGuide())
	}
}

// TestCodexDesktopStopPrefersConcurrentTerminal 验证远程中断期间自然完成时不会误报停止成功。
func TestCodexDesktopStopPrefersConcurrentTerminal(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexRuntimeDesktop,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	ag.interruptHook = func() { task.claimTerminal() }

	text, handled := h.interruptExternalCodexTask(externalCodexTaskCommand{
		ctx: context.Background(), key: route.conversationID, agent: ag, actor: "user-1",
	})

	if !handled || text != "当前任务已经结束，无需停止。" {
		t.Fatalf("handled=%v text=%q", handled, text)
	}
	if taskPhase(task) != codexTaskTerminal {
		t.Fatalf("phase=%s，停止请求不应覆盖并发终态", taskPhase(task))
	}
}

// TestCodexDesktopStopRollsBackFailedRequest 验证协议拒绝中断后任务仍保持可控制状态。
func TestCodexDesktopStopRollsBackFailedRequest(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexRuntimeDesktop,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	ag.interruptErr = errors.New("interrupt rejected")

	text, handled := h.interruptExternalCodexTask(externalCodexTaskCommand{
		ctx: context.Background(), key: route.conversationID, agent: ag, actor: "user-1",
	})

	if !handled || !strings.Contains(text, "interrupt rejected") {
		t.Fatalf("handled=%v text=%q", handled, text)
	}
	if taskPhase(task) != codexTaskRunning || task.stopRequested {
		t.Fatalf("phase=%s stopRequested=%v", taskPhase(task), task.stopRequested)
	}
}

// TestCodexDesktopRepeatedStopDoesNotRepeatInterrupt 验证重复停止只等待既有请求。
func TestCodexDesktopRepeatedStopDoesNotRepeatInterrupt(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexRuntimeDesktop,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	req := externalCodexTaskCommand{
		ctx: context.Background(), key: route.conversationID, agent: ag, actor: "user-1",
	}

	first, firstHandled := h.interruptExternalCodexTask(req)
	second, secondHandled := h.interruptExternalCodexTask(req)

	if !firstHandled || !secondHandled || !strings.Contains(first, "等待任务终态") || !strings.Contains(second, "等待任务终态") {
		t.Fatalf("first=%q second=%q", first, second)
	}
	if ag.interruptCalls != 1 {
		t.Fatalf("interrupt calls=%d, want 1", ag.interruptCalls)
	}
}

func TestCodexPendingMessageRechecksOwnerBeforeAutorun(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	pending := h.pendingCodexTask(opts)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)
	ag.bindErr = agent.ErrCodexDesktopDisconnected
	pending.run()
	waitUntil(t, func() bool { return len(opts.reply.(*platformtest.Replier).Texts) > 0 })
	if ag.chatCallCount() != 0 {
		t.Fatal("pending 在 owner 断线后仍开始了新 turn")
	}
}

func TestCodexMessageRestoresPersistedRemoteRuntimeAfterRestart(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)

	h.startCodexAgentTask(opts)

	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
	waitUntil(t, func() bool {
		_, active := h.activeTask(route.conversationID)
		return !active
	})
	if ag.handoffCalls != 1 || ag.lastTurnReq.Runtime.Intent.RouteKey != route.bindingKey {
		t.Fatalf("handoff=%d turn=%#v", ag.handoffCalls, ag.lastTurnReq)
	}
}

func TestUnauthorizedUserCannotGuideStopOrReadPendingAction(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	h.agents["codex"] = ag
	h.defaultName = "codex"
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexRuntimeDesktop,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	h.storePendingGuide(route.conversationID, pendingAgentTask{message: "私有指令", run: func() {}})
	guide, handled := h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{ctx: context.Background(), key: route.conversationID, agentName: "codex", actor: "user-2"})
	stop, stopHandled := h.interruptExternalCodexTask(externalCodexTaskCommand{ctx: context.Background(), key: route.conversationID, agent: ag, actor: "user-2"})
	if !handled || !stopHandled || !strings.Contains(guide, "只有任务发起人") || !strings.Contains(stop, "只有任务发起人") {
		t.Fatalf("guide=%q stop=%q", guide, stop)
	}
	if task.pendingGuide() != "私有指令" || ag.interruptTurnID != "" || ag.steerTurnID != "" {
		t.Fatal("未授权控制读取或消费了 pending action")
	}
}

func liveMessageFixture(t *testing.T, active bool) (*Handler, *fakeCodexLiveAgent, codexAgentTaskOptions, codexConversationRoute) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	state := agent.CodexThreadState{ThreadID: "thread-1", Active: active, Model: "gpt-live"}
	if active {
		state.ActiveTurnID = "turn-1"
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, state)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-1")
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: "user-1", agentName: "codex", bindingKey: bindingKey,
		workspace: workspace, threadID: "thread-1",
	})
	route := h.codexConversationRouteForSession("user-1", "user-1", "codex", ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	opts := codexAgentTaskOptions{
		ctx: context.Background(), userID: "user-1", routeUserID: "user-1", reply: reply,
		agentName: "codex", message: "继续任务", agent: ag, progressCfg: cfg, route: route,
	}
	return h, ag, opts, route
}
