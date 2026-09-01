package messaging

import (
	"context"
	"errors"
	"strings"
	"sync"
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

func TestCodexDesktopActiveMessageSteersCurrentTurn(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, true)
	ag.watchDone = make(chan struct{})
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool {
		_, _, message := ag.steerSnapshot()
		return message == "继续任务"
	})
	task, _ := h.activeTask(route.conversationID)
	defer task.cancel()
	if ag.chatCallCount() != 0 {
		t.Fatal("active Desktop thread 不应开始新 turn")
	}
	steerThreadID, steerTurnID, steerMessage := ag.steerSnapshot()
	if task.pendingGuide() != "" || steerThreadID != route.threadID || steerTurnID != "turn-1" {
		t.Fatalf("pending=%q steer=(%q,%q,%q)", task.pendingGuide(), steerThreadID, steerTurnID, steerMessage)
	}
	text := strings.Join(opts.reply.(*platformtest.Replier).TextsSnapshot(), "\n")
	if !strings.Contains(text, "已作为补充输入加入当前任务") || strings.Contains(text, queuedAgentMessage) {
		t.Fatalf("reply=%q", text)
	}
}

func TestCodexDesktopActiveMessageClaimsStableFollowerTerminalDelivery(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, true)
	ag.watchDone = make(chan struct{})
	t.Cleanup(func() { closeTestChannel(ag.watchDone) })
	deliveryRoute := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}
	initial := h.ensureCodexSessions().remoteSelectionSnapshot(route.bindingKey, route.threadID)
	if _, err := h.ensureCodexSessions().commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: route.bindingKey, WorkspaceRoot: route.workspaceRoot,
		TargetThreadID: route.threadID, ConversationID: route.conversationID,
		SetFollower: true,
		Follower: &codexFrontendFollower{
			WorkspaceRoot: route.workspaceRoot, ThreadID: route.threadID,
			ActorUserID: opts.userID, AuthorizedIdentity: opts.userID, DeliveryRoute: deliveryRoute,
		},
		Expected: initial,
	}); err != nil {
		t.Fatal(err)
	}
	preparing, ok := h.ensureCodexSessions().followerSnapshot(route.bindingKey)
	if !ok {
		t.Fatal("committed follower is missing")
	}
	prepared, err := h.ensureCodexSessions().commitFollowerAttachRuntime(preparing, "turn-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ensureCodexSessions().commitFollowerAttachReady(prepared, "turn-1", 0); err != nil {
		t.Fatal(err)
	}
	opts.platform = platform.PlatformFeishu
	opts.accountID = "cli_a"
	opts.reply = &codexFollowerRouteReplier{Replier: opts.reply.(*platformtest.Replier), route: deliveryRoute}

	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool {
		_, _, message := ag.steerSnapshot()
		return message == "继续任务"
	})
	task, ok := h.activeTask(route.conversationID)
	if !ok {
		t.Fatal("active steer did not register its observer")
	}
	snapshot, ok := h.ensureCodexSessions().followerSnapshot(route.bindingKey)
	if !ok || snapshot.FollowTurnID != "turn-1" || !snapshot.FollowTurnPending {
		t.Fatalf("steer follower snapshot=%#v ok=%v, want pending turn-1", snapshot, ok)
	}
	wantKey := codexFollowerTerminalOutboxID(snapshot, "turn-1")
	gotKey := task.terminalDeliveryKeySnapshot()
	if gotKey != wantKey {
		t.Fatalf("terminal delivery key=%q, want %q", gotKey, wantKey)
	}
}

func TestCodexActiveInputReleasesThreadLockAfterUnifiedDispatch(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, true)
	ag.watchDone = make(chan struct{})
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return ag.steerInputCallCount() == 1 })
	if task, ok := h.activeTask(route.conversationID); ok {
		task.cancel()
	}
	assertCodexThreadLockReusable(t, h, route.threadID)
}

func TestCodexActiveInputUsesBoundedControlContext(t *testing.T) {
	h, ag, first, route := liveMessageFixture(t, false)
	h.codexControlTimeout = 50 * time.Millisecond
	turnEntered := make(chan struct{}, 1)
	turnRelease := make(chan struct{})
	ag.turnEntered, ag.turnRelease = turnEntered, turnRelease
	h.startCodexAgentTask(first)
	select {
	case <-turnEntered:
	case <-time.After(taskWaitTimeout):
		t.Fatal("first Codex turn did not enter the blocking fixture")
	}
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: route.threadID, Active: true, ActiveTurnID: "turn-1",
	})
	second := first
	second.message = "bounded steer"
	second.reply = platformtest.NewReplier(platform.Capabilities{Text: true})

	h.startCodexAgentTask(second)

	waitUntil(t, func() bool { return ag.steerInputCallCount() == 1 })
	if !ag.steerInputDeadlineSnapshot() {
		t.Fatal("active input submission must inherit the bounded thread-control context")
	}
	close(turnRelease)
	waitUntil(t, func() bool { _, active := h.activeTask(route.conversationID); return !active })
}

func TestCodexInProcessActiveTaskSteersSecondMessage(t *testing.T) {
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
	second.message, second.reply, second.messageKey = "第二条", secondReply, "message-second"
	h.startCodexAgentTask(second)
	text := strings.Join(secondReply.TextsSnapshot(), "\n")
	if !strings.Contains(text, "已发送到当前共享 Codex 任务") || strings.Contains(text, queuedAgentMessage) || strings.Contains(text, "暂不能开始任务") {
		t.Fatalf("第二条应直接进入现有 in-process turn，reply=%q", text)
	}
	steerThreadID, steerTurnID, steerMessage := ag.steerSnapshot()
	if steerThreadID != route.threadID || steerTurnID != "turn-1" || steerMessage != "第二条" {
		t.Fatalf("steer=(%q,%q,%q)", steerThreadID, steerTurnID, steerMessage)
	}
	if calls := ag.steerInputCallCount(); calls != 1 {
		t.Fatalf("protected steer calls=%d, want 1", calls)
	}
	attempts := h.ensureCodexInputAttempts().snapshot()
	if len(attempts) != 1 || attempts[0].MessageKey != "message-second" ||
		attempts[0].Status != agent.CodexInputAttemptAccepted {
		t.Fatalf("input attempts=%#v", attempts)
	}
	idleState := agent.CodexThreadState{ThreadID: route.threadID, Model: "gpt-live"}
	ag.setBindingState(idleState)
	close(turnRelease)
	waitUntil(t, func() bool { _, active := h.activeTask(route.conversationID); return !active })
	if ag.chatCallCount() != 1 {
		t.Fatalf("run calls=%d, want only the original turn", ag.chatCallCount())
	}
}

func TestCodexActiveInputUnknownDeliveryIsNotQueuedOrRetried(t *testing.T) {
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
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: route.threadID, Active: true, ActiveTurnID: "turn-1",
	})
	ag.steerInputErr = agent.ErrCodexInputDeliveryUnconfirmed
	secondReply := platformtest.NewReplier(platform.Capabilities{Text: true})
	second := first
	second.message, second.reply, second.messageKey = "未知交付补充", secondReply, "message-unknown"

	h.startCodexAgentTask(second)

	text := strings.Join(secondReply.TextsSnapshot(), "\n")
	if !strings.Contains(text, "没有自动重发") || !strings.Contains(text, "Codex App") {
		t.Fatalf("reply=%q", text)
	}
	_, _, steerMessage := ag.steerSnapshot()
	if calls := ag.steerInputCallCount(); calls != 1 || steerMessage != "" {
		t.Fatalf("protected calls=%d low-level steer=%q, want one unconfirmed submission and no retry", calls, steerMessage)
	}
	if task, ok := h.activeTask(route.conversationID); !ok {
		t.Fatal("original active task disappeared after unknown delivery")
	} else if pending := task.pendingGuide(); pending != "" {
		t.Fatalf("pending=%q, unknown delivery must not be queued", pending)
	}
	attempts := h.ensureCodexInputAttempts().snapshot()
	if len(attempts) != 1 || attempts[0].Status != agent.CodexInputAttemptUnconfirmed {
		t.Fatalf("input attempts=%#v", attempts)
	}

	close(turnRelease)
	waitUntil(t, func() bool { _, active := h.activeTask(route.conversationID); return !active })
}

func TestCodexIdleAuthorityWaitsForLocalTaskReleaseInsteadOfQueueing(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	task, _ := newActiveAgentTask(context.Background(), activeTaskMeta{
		owner: opts.userID, routeUserID: opts.routeUserID, agentName: opts.agentName,
		codexThreadID: route.threadID,
	})
	result := make(chan bool, 1)
	go func() {
		result <- h.steerMessageIntoLiveTask(codexTaskPreflightOptions{
			ctx: context.Background(), taskOpts: opts, route: route, cancel: func() {},
		}, task)
	}()
	waitUntil(t, func() bool { return ag.steerInputCallCount() == 1 })

	select {
	case handled := <-result:
		close(task.done)
		t.Fatalf("handled=%v, idle authority returned before the stale local lifecycle was released", handled)
	case <-time.After(50 * time.Millisecond):
	}

	close(task.done)
	select {
	case handled := <-result:
		if handled {
			t.Fatal("released local lifecycle should let the caller start a new turn")
		}
	case <-time.After(taskWaitTimeout):
		t.Fatal("idle input did not resume after the stale local lifecycle was released")
	}
	if pending := task.pendingGuide(); pending != "" {
		t.Fatalf("pending=%q, idle input must not enter the old task queue", pending)
	}
}

func TestCodexIdleAuthorityDoesNotQueueWhenLocalTaskReleaseTimesOut(t *testing.T) {
	h, _, opts, route := liveMessageFixture(t, false)
	task, _ := newActiveAgentTask(context.Background(), activeTaskMeta{
		owner: opts.userID, routeUserID: opts.routeUserID, agentName: opts.agentName,
		codexThreadID: route.threadID,
	})
	defer close(task.done)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handled := h.steerMessageIntoLiveTask(codexTaskPreflightOptions{
		ctx: ctx, taskOpts: opts, route: route, cancel: func() {},
	}, task)

	text := strings.Join(opts.reply.(*platformtest.Replier).TextsSnapshot(), "\n")
	if !handled || !strings.Contains(text, "输入未发送且不会排队") {
		t.Fatalf("handled=%v reply=%q", handled, text)
	}
	if pending := task.pendingGuide(); pending != "" {
		t.Fatalf("pending=%q, timed out input must not be retained", pending)
	}
}

func TestCodexDesktopDirectInputKeepsResolvedStreamProgress(t *testing.T) {
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
		t.Fatalf("typing=%#v，外部任务未继承 stream 配置", reply.TypingStatesSnapshot())
	}
	task, ok := h.activeTask(route.conversationID)
	if !ok {
		t.Fatal("直接输入后外部任务未登记")
	}
	defer task.cancel()
	if reply.Stream.Options.Title == "" {
		t.Fatalf("typing=%#v，外部任务未继承 stream 配置", reply.TypingStatesSnapshot())
	}
	if states := reply.TypingStatesSnapshot(); len(states) != 0 {
		t.Fatalf("typing=%#v，stream 模式不应创建 typing 卡", states)
	}
}

func TestCodexUnavailableHostDoesNotQueueOrWriteInput(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)
	ag.runErr = agent.ErrCodexRuntimeUnavailable
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { _, active := h.activeTask(route.conversationID); return !active })
	text := strings.Join(opts.reply.(*platformtest.Replier).TextsSnapshot(), "\n")
	_, steerTurnID, _ := ag.steerSnapshot()
	if ag.chatCallCount() != 0 || steerTurnID != "" {
		t.Fatalf("chat=%d steer=%q", ag.chatCallCount(), steerTurnID)
	}
	if _, active := h.activeTask(route.conversationID); active {
		t.Fatal("unavailable Host failure must not leave an active or queued task")
	}
	if !strings.Contains(text, "运行通道暂不可用") || !strings.Contains(text, "绑定保持不变") {
		t.Fatalf("reply=%q, want binding-preserving unavailable notice", text)
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
	_, steerTurnID, _ := ag.steerSnapshot()
	if !handled || !strings.Contains(text, "已发送") || steerTurnID != "turn-1" {
		t.Fatalf("handled=%v text=%q turn=%q", handled, text, steerTurnID)
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
	if !handled || !strings.Contains(text, "等待任务结束") {
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

	if !firstHandled || !secondHandled || !strings.Contains(first, "等待任务结束") || !strings.Contains(second, "等待任务结束") {
		t.Fatalf("first=%q second=%q", first, second)
	}
	if ag.interruptCalls != 1 {
		t.Fatalf("interrupt calls=%d, want 1", ag.interruptCalls)
	}
}

func TestCodexPendingMessageDoesNotWriteWhenHostBecomesUnavailable(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	pending := h.pendingCodexTask(opts)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)
	ag.runErr = agent.ErrCodexRuntimeUnavailable
	pending.run()
	waitUntil(t, func() bool { _, active := h.activeTask(route.conversationID); return !active })
	if ag.chatCallCount() != 0 {
		t.Fatal("Host unavailable 时 pending 输入不应启动新 turn")
	}
}

func TestCodexRuntimeReadErrorDoesNotWriteOrLeaveQueuedTask(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	ag.runErr = agent.ErrCodexDesktopOwnershipUnknown

	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { _, active := h.activeTask(route.conversationID); return !active })

	_, steerTurnID, _ := ag.steerSnapshot()
	if ag.chatCallCount() != 0 || steerTurnID != "" {
		t.Fatalf("runtime read error started work: chat=%d steer=%q", ag.chatCallCount(), steerTurnID)
	}
	if _, active := h.activeTask(route.conversationID); active {
		t.Fatal("runtime read error must not leave an active or queued task")
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
	_, steerTurnID, _ := ag.steerSnapshot()
	if task.pendingGuide() != "私有指令" || ag.interruptTurnID != "" || steerTurnID != "" {
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

type guideSteerCall struct {
	threadID string
	turnID   string
	message  string
}

type recordingGuideAgent struct {
	*fakeCodexLiveAgent
	guideMu sync.Mutex
	guides  []guideSteerCall
}

func (a *recordingGuideAgent) SteerCodexThread(_ context.Context, _ string, threadID string, turnID string, message string) error {
	a.guideMu.Lock()
	a.guides = append(a.guides, guideSteerCall{threadID: threadID, turnID: turnID, message: message})
	a.fakeCodexThreadAgent.steerMu.Lock()
	a.fakeCodexThreadAgent.steerThreadID = threadID
	a.fakeCodexThreadAgent.steerTurnID = turnID
	a.fakeCodexThreadAgent.steerMessage = message
	err := a.fakeCodexThreadAgent.steerErr
	a.fakeCodexThreadAgent.steerMu.Unlock()
	a.guideMu.Unlock()
	return err
}

func (a *recordingGuideAgent) SteerCodexInput(_ context.Context, req agent.CodexTurnRequest) (string, error) {
	turnID := strings.TrimSpace(a.threadBinding(req.Runtime.Ref.ThreadID).State.ActiveTurnID)
	if turnID == "" {
		return "", agent.ErrCodexNoActiveTurn
	}
	a.guideMu.Lock()
	a.guides = append(a.guides, guideSteerCall{
		threadID: req.Runtime.Ref.ThreadID, turnID: turnID, message: req.Message,
	})
	a.fakeCodexThreadAgent.steerMu.Lock()
	a.fakeCodexThreadAgent.steerThreadID = req.Runtime.Ref.ThreadID
	a.fakeCodexThreadAgent.steerTurnID = turnID
	a.fakeCodexThreadAgent.steerMessage = req.Message
	err := a.fakeCodexThreadAgent.steerErr
	a.fakeCodexThreadAgent.steerMu.Unlock()
	a.guideMu.Unlock()
	if err != nil {
		return "", err
	}
	if req.OnTurnSteered != nil {
		if err := req.OnTurnSteered(req.Runtime.Ref, turnID); err != nil {
			return "", err
		}
	}
	return turnID, nil
}

func (a *recordingGuideAgent) guideSnapshot() []guideSteerCall {
	a.guideMu.Lock()
	defer a.guideMu.Unlock()
	return append([]guideSteerCall(nil), a.guides...)
}

type guideRelayTestReplier struct {
	*reanchorTestReplier
	textsMu      sync.Mutex
	texts        []string
	openErr      error
	openAttempts int
}

func newGuideRelayTestReplier(cardID string) *guideRelayTestReplier {
	inner := newReanchorTestReplier()
	inner.cardID = cardID
	inner.stream.cardID = cardID
	return &guideRelayTestReplier{reanchorTestReplier: inner}
}

func (r *guideRelayTestReplier) SendText(_ context.Context, text string) error {
	r.textsMu.Lock()
	r.texts = append(r.texts, text)
	r.textsMu.Unlock()
	return nil
}

func (r *guideRelayTestReplier) OpenStream(ctx context.Context, options platform.StreamOptions) (platform.Stream, error) {
	r.openAttempts++
	if r.openErr != nil {
		return nil, r.openErr
	}
	return r.reanchorTestReplier.OpenStream(ctx, options)
}

func (r *guideRelayTestReplier) textsSnapshot() []string {
	r.textsMu.Lock()
	defer r.textsMu.Unlock()
	return append([]string(nil), r.texts...)
}

type liveGuideRelayFixture struct {
	h        *Handler
	agent    *recordingGuideAgent
	opts     codexAgentTaskOptions
	route    codexConversationRoute
	task     *activeAgentTask
	progress *progressSession
	finish   func(string, bool) bool
	oldReply *guideRelayTestReplier
}

func newLiveGuideRelayFixture(t *testing.T, nativeProgress bool) liveGuideRelayFixture {
	t.Helper()
	h, base, opts, route := liveMessageFixture(t, true)
	recording := &recordingGuideAgent{fakeCodexLiveAgent: base}
	h.agents["codex"] = recording
	opts.agent = recording
	task, taskCtx, started := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: opts.userID, routeUserID: opts.routeUserID, agentName: opts.agentName,
		message: "活动任务", runtimeOwner: agent.CodexRuntimeWeClaw,
		codexThreadID: route.threadID, codexTurnID: "turn-1", inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("failed to register active guide task")
	}
	fixture := liveGuideRelayFixture{h: h, agent: recording, opts: opts, route: route, task: task}
	if !nativeProgress {
		return fixture
	}
	oldReply := newGuideRelayTestReplier("card-initial")
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, finish, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		taskCtx, oldReply, "", "codex", route.workspaceRoot, "活动任务", cfg,
	)
	task.setProgressTimelineLimit(cfg.EffectiveStreamTimelineLimit())
	task.attachProgressSession(progress)
	fixture.progress, fixture.finish, fixture.oldReply = progress, finish, oldReply
	fixture.opts.progressCfg = cfg
	return fixture
}

func (f liveGuideRelayFixture) sendGuide(message string, messageKey string, reply platform.Replier) {
	opts := f.opts
	opts.message = message
	opts.messageKey = messageKey
	opts.reply = reply
	f.h.startCodexAgentTask(opts)
}

func TestLiveCodexGuideCreatesRelayCardWithoutSuccessText(t *testing.T) {
	fixture := newLiveGuideRelayFixture(t, true)
	reply := newGuideRelayTestReplier("card-guide-1")
	fixture.sendGuide("补充要求一", "feishu\x00cli_a\x00message-1", reply)

	if calls := fixture.agent.guideSnapshot(); len(calls) != 1 || calls[0].message != "补充要求一" {
		t.Fatalf("steer calls=%#v", calls)
	}
	if reply.openAttempts != 1 || fixture.oldReply.stream.supersededCount() != 1 {
		t.Fatalf("open=%d old superseded=%d", reply.openAttempts, fixture.oldReply.stream.supersededCount())
	}
	if texts := reply.textsSnapshot(); len(texts) != 0 {
		t.Fatalf("successful relay should not send text: %#v", texts)
	}
}

func TestThreeRapidLiveCodexGuidesCreateThreeOrderedRelayCards(t *testing.T) {
	fixture := newLiveGuideRelayFixture(t, true)
	replies := []*guideRelayTestReplier{
		newGuideRelayTestReplier("card-guide-1"),
		newGuideRelayTestReplier("card-guide-2"),
		newGuideRelayTestReplier("card-guide-3"),
	}
	for index, reply := range replies {
		fixture.sendGuide(
			[]string{"第一条引导", "第二条引导", "第三条引导"}[index],
			[]string{"message-1", "message-2", "message-3"}[index],
			reply,
		)
	}

	calls := fixture.agent.guideSnapshot()
	if len(calls) != 3 || calls[0].message != "第一条引导" || calls[1].message != "第二条引导" || calls[2].message != "第三条引导" {
		t.Fatalf("steer order=%#v", calls)
	}
	streams := []*reanchorTestStream{fixture.oldReply.stream, replies[0].stream, replies[1].stream, replies[2].stream}
	for index := 0; index < len(streams)-1; index++ {
		if streams[index].supersededCount() != 1 {
			t.Fatalf("stream %d superseded=%d, want 1", index, streams[index].supersededCount())
		}
	}
	if streams[3].supersededCount() != 0 {
		t.Fatalf("latest stream superseded=%d", streams[3].supersededCount())
	}
	for index, reply := range replies {
		if reply.openAttempts != 1 || len(reply.textsSnapshot()) != 0 {
			t.Fatalf("reply %d open=%d texts=%#v", index, reply.openAttempts, reply.textsSnapshot())
		}
	}
	if !fixture.progress.send("后续进展") || !fixture.finish("最终结果", false) {
		t.Fatal("latest relay should accept progress and terminal")
	}
	for index := 0; index < len(streams)-1; index++ {
		if streams[index].completedCount() != 0 || len(streams[index].updateSnapshot()) != 0 {
			t.Fatalf("old stream %d completed=%d updates=%#v", index, streams[index].completedCount(), streams[index].updateSnapshot())
		}
	}
	if streams[3].completedCount() != 1 || len(streams[3].updateSnapshot()) != 1 {
		t.Fatalf("latest completed=%d updates=%#v", streams[3].completedCount(), streams[3].updateSnapshot())
	}
}

func TestLiveCodexGuideSteerFailureDoesNotCreateRelayCard(t *testing.T) {
	fixture := newLiveGuideRelayFixture(t, true)
	fixture.agent.fakeCodexThreadAgent.steerErr = errors.New("steer rejected")
	reply := newGuideRelayTestReplier("card-guide-failed")
	fixture.sendGuide("失败引导", "message-failed", reply)

	if reply.openAttempts != 0 || fixture.oldReply.stream.supersededCount() != 0 {
		t.Fatalf("failed steer opened=%d superseded=%d", reply.openAttempts, fixture.oldReply.stream.supersededCount())
	}
	if texts := strings.Join(reply.textsSnapshot(), "\n"); !strings.Contains(texts, "steer rejected") {
		t.Fatalf("failure reply=%q", texts)
	}
}

func TestLiveCodexGuideReanchorFailureWarnsWithoutRepeatingSteer(t *testing.T) {
	fixture := newLiveGuideRelayFixture(t, true)
	reply := newGuideRelayTestReplier("card-guide-failed")
	reply.openErr = errors.New("create card failed")
	fixture.sendGuide("已送达引导", "message-reanchor-failed", reply)

	if calls := fixture.agent.guideSnapshot(); len(calls) != 1 {
		t.Fatalf("steer calls=%#v", calls)
	}
	if reply.openAttempts != 1 || fixture.oldReply.stream.supersededCount() != 0 {
		t.Fatalf("open=%d old superseded=%d", reply.openAttempts, fixture.oldReply.stream.supersededCount())
	}
	text := strings.Join(reply.textsSnapshot(), "\n")
	if !strings.Contains(text, "引导已送达，但任务卡迁移失败") || !strings.Contains(text, "create card failed") {
		t.Fatalf("warning=%q", text)
	}
}

func TestLiveCodexGuideWithoutNativeProgressCardKeepsSuccessText(t *testing.T) {
	fixture := newLiveGuideRelayFixture(t, false)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	fixture.sendGuide("文本引导", "message-text-only", reply)

	if calls := fixture.agent.guideSnapshot(); len(calls) != 1 {
		t.Fatalf("steer calls=%#v", calls)
	}
	if text := strings.Join(reply.TextsSnapshot(), "\n"); !strings.Contains(text, "已发送到当前共享 Codex 任务") {
		t.Fatalf("success reply=%q", text)
	}
}
