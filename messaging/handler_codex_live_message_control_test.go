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
	if ag.bindCalls != 0 || ag.handoffCalls != 0 || ag.lastChatMessage() != "继续任务" {
		t.Fatalf("bind=%d message=%q", ag.bindCalls, ag.lastChatMessage())
	}
}

func TestCodexLiveTaskCompletionKeepsExplicitThreadSelection(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	ag.fakeCodexThreadAgent.threadID = "thread-stale"

	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
	waitUntil(t, func() bool {
		_, active := h.activeTask(route.conversationID)
		return !active
	})

	threadID, pending := h.ensureCodexSessions().getThread(route.bindingKey, route.workspaceRoot)
	if pending || threadID != route.threadID {
		t.Fatalf("thread=%q pending=%v，任务完成不应让 ACP 旧映射覆盖显式选择 %q", threadID, pending, route.threadID)
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

func TestCodexInProcessActiveTaskQueuesSecondMessage(t *testing.T) {
	h, ag, first, route := liveMessageFixture(t, false)
	turnEntered := make(chan struct{}, 1)
	turnRelease := make(chan struct{})
	ag.turnEntered, ag.turnRelease = turnEntered, turnRelease
	first.message = "第一条"
	h.startCodexAgentTask(first)
	select {
	case <-turnEntered:
	case <-time.After(taskWaitTimeout):
		t.Fatal("第一条 in-process Codex 任务未进入阻塞执行")
	}
	activeState := agent.CodexThreadState{
		ThreadID: route.threadID, Active: true, ActiveTurnID: "turn-1", Model: "gpt-live",
	}
	ag.setBindingState(activeState)
	secondReply := platformtest.NewReplier(platform.Capabilities{Text: true})
	second := first
	second.message, second.reply = "第二条", secondReply
	h.startCodexAgentTask(second)
	text := strings.Join(secondReply.Texts, "\n")
	if !strings.Contains(text, queuedAgentMessage) || strings.Contains(text, "reservation") || strings.Contains(text, "暂不能开始任务") {
		t.Fatalf("第二条应进入现有 in-process 队列，reply=%q", text)
	}
	idleState := agent.CodexThreadState{ThreadID: route.threadID, Model: "gpt-live"}
	ag.setBindingState(idleState)
	close(turnRelease)
	waitUntil(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return ag.runCalls == 2
	})
	waitUntil(t, func() bool { _, active := h.activeTask(route.conversationID); return !active })
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

func TestCodexUnknownRuntimeStartsWithoutChangingOwner(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
	text := strings.Join(opts.reply.(*platformtest.Replier).Texts, "\n")
	if ag.bindCalls != 0 || ag.handoffCalls != 0 || strings.Contains(text, "运行通道暂不可用") {
		t.Fatalf("chat=%d inspect=%d handoff=%d text=%q", ag.chatCallCount(), ag.bindCalls, ag.handoffCalls, text)
	}
	if ag.lastTurnReq.Runtime.PendingFirstTurn {
		t.Fatal("普通历史 thread 不能仅因本地 rollout 为空获得自动补建资格")
	}
}

func TestCodexDesktopGuideUsesCurrentTurn(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	h.agents["codex"] = ag
	h.defaultName = "codex"
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexRuntimeWeClaw,
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
		owner: "user-1", runtimeOwner: agent.CodexRuntimeWeClaw,
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
		owner: "user-1", runtimeOwner: agent.CodexRuntimeWeClaw,
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
		owner: "user-1", runtimeOwner: agent.CodexRuntimeWeClaw,
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
		owner: "user-1", runtimeOwner: agent.CodexRuntimeWeClaw,
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

func TestCodexPendingMessageKeepsRemoteOwnerPriority(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	pending := h.pendingCodexTask(opts)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)
	pending.run()
	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
	if ag.chatCallCount() != 1 {
		t.Fatal("pending 在 remote owner 仍有效时应继续启动新 turn")
	}
}

func TestCodexMessageDoesNotLetUnknownRuntimeVetoRemoteOwner(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)

	h.startCodexAgentTask(opts)

	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
	if ag.bindCalls != 0 || ag.handoffCalls != 0 {
		t.Fatalf("handler 不应把 runtime 快照当授权门禁，chat=%d inspect=%d handoff=%d", ag.chatCallCount(), ag.bindCalls, ag.handoffCalls)
	}
}

func TestUnauthorizedUserCannotGuideStopOrReadPendingAction(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	h.agents["codex"] = ag
	h.defaultName = "codex"
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexRuntimeWeClaw,
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
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-1")
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
