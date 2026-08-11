package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type feishuExternalProgressFixture struct {
	h         *Handler
	workspace string
	watchDone chan struct{}
	reply     *platformtest.Replier
}

// newFeishuExternalProgressFixture 创建关闭飞书进度的外部 Codex 任务场景。
func newFeishuExternalProgressFixture(t *testing.T) feishuExternalProgressFixture {
	t.Helper()
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	offCfg := config.DefaultProgressConfig()
	offCfg.Mode = progressModeOff
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_a"): offCfg,
	})
	watchDone := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-watchDone:
		default:
			close(watchDone)
		}
	})
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active", Preview: "本地 App 发起的任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	ag.watchReply, ag.watchDone = "本地任务完成", watchDone
	h.defaultName = "codex"
	h.agents["codex"] = ag
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	return feishuExternalProgressFixture{h: h, workspace: workspace, watchDone: watchDone, reply: reply}
}

func TestCodexExternalAppTaskUsesFeishuAccountProgress(t *testing.T) {
	fixture := newFeishuExternalProgressFixture(t)
	fixture.h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_user", Text: "/cx cd weclaw",
	}, fixture.reply)
	close(fixture.watchDone)
	waitUntil(t, func() bool {
		_, active := fixture.h.activeTask(buildCodexConversationID("ou_user", "codex", fixture.workspace))
		return !active
	})
	if !containsText(fixture.reply.Texts, "本地任务完成") {
		t.Fatalf("texts=%#v, want final text reply", fixture.reply.Texts)
	}
	if fixture.reply.Stream.Completed != "" {
		t.Fatalf("completed=%q, want no stream completion when account progress is off", fixture.reply.Stream.Completed)
	}
}

func TestCodexSwitchHidesAppThreadStateReadError(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-active"})
	ag.threadStateErr = errors.New("app-server unavailable")
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(169, "/cx cd weclaw"))
	if text := strings.Join(calls.texts(), "\n"); !strings.Contains(text, "窗口绑定已保留") || strings.Contains(text, "app-server unavailable") {
		t.Fatalf("切换响应不得暴露状态读取失败，messages=%#v", calls.texts())
	}
}

func TestCodexSwitchHidesMissingActiveTurnError(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	state := agent.CodexThreadState{ThreadID: "thread-active", Active: true, Preview: "本地 App 发起的任务"}
	h.agents["codex"] = newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(170, "/cx cd weclaw"))
	key := buildCodexConversationID("user-1", "codex", workspace)
	if _, ok := h.activeTask(key); ok {
		t.Fatal("缺少 active turn 时不应登记外部任务镜像")
	}
	if text := strings.Join(calls.texts(), "\n"); !strings.Contains(text, "窗口绑定已保留") || strings.Contains(text, "active turn") {
		t.Fatalf("切换响应不得暴露 active turn 细节，messages=%#v", calls.texts())
	}
}

func TestCodexStopInterruptsExternalActiveTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active", Preview: "本地 App 发起的任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	ag.watchDone = make(chan struct{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(167, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(168, "/stop"))
	if ag.interruptThreadID != "thread-active" || ag.interruptTurnID != "turn-active" {
		t.Fatalf("interrupt=(%q,%q), want active thread turn", ag.interruptThreadID, ag.interruptTurnID)
	}
	if !containsText(calls.texts(), "同时停止本地 Codex 和当前消息窗口中的共享任务") {
		t.Fatalf("/stop should confirm interrupt and wait for terminal, messages=%#v", calls.texts())
	}
}

func TestFeishuStopResolvesInProcessUnknownRuntime(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, false)
	h.defaultName = "codex"
	h.agents["codex"] = ag
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: route.threadID, Active: true, ActiveTurnID: "turn-fake",
	})
	task, _, started := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", routeUserID: "user-1", agentName: "codex",
		runtimeOwner:  agent.CodexRuntimeUnknown,
		codexThreadID: route.threadID, codexTurnID: "turn-fake",
		inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("failed to register in-process Codex task")
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android",
		UserID: "user-1", MessageID: "stop-in-process", Text: "/stop",
	}, reply)

	if ag.interruptThreadID != route.threadID || ag.interruptTurnID != "turn-fake" {
		t.Fatalf("interrupt=(%q,%q), want (%q,%q)", ag.interruptThreadID, ag.interruptTurnID, route.threadID, "turn-fake")
	}
	if !containsText(reply.Texts, "同时停止本地 Codex 和当前消息窗口中的共享任务") {
		t.Fatalf("reply=%#v, want remote stop confirmation", reply.Texts)
	}
	if taskPhase(task) != codexTaskStopping || task.runtimeOwner != agent.CodexRuntimeWeClaw {
		t.Fatalf("phase=%s runtime=%s, want stopping/weclaw", taskPhase(task), task.runtimeOwner)
	}
}

func TestCodexStopRejectsUnknownInProcessTaskWhenProbeFindsDesktop(t *testing.T) {
	h := NewHandler(nil, nil)
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, state)
	task, _, started := h.beginActiveTask(context.Background(), "conversation-1", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "codex",
		runtimeOwner:  agent.CodexRuntimeUnknown,
		codexThreadID: "thread-active", codexTurnID: "turn-active",
		inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("failed to register in-process Codex task")
	}

	text, handled := h.interruptExternalCodexTask(externalCodexTaskCommand{
		ctx: context.Background(), key: "conversation-1", agent: ag, actor: "user-1",
	})

	if !handled || !strings.Contains(text, "实时运行位置不可用") {
		t.Fatalf("handled=%v text=%q, want fail-closed runtime error", handled, text)
	}
	if ag.interruptCalls != 0 {
		t.Fatalf("interrupt calls=%d, Desktop runtime must not be interrupted through WeClaw", ag.interruptCalls)
	}
	if taskPhase(task) != codexTaskRunning || task.runtimeOwner != agent.CodexRuntimeUnknown {
		t.Fatalf("phase=%s runtime=%s, failed probe must not mutate task", taskPhase(task), task.runtimeOwner)
	}
}

func TestCodexStopDoesNotRefreshRuntimeAfterConcurrentTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	ag.threadStateEntered = make(chan struct{}, 1)
	threadStateRelease := make(chan struct{})
	ag.threadStateRelease = threadStateRelease
	task, _, started := h.beginActiveTask(context.Background(), "conversation-1", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "codex",
		runtimeOwner:  agent.CodexRuntimeUnknown,
		codexThreadID: "thread-active", codexTurnID: "turn-active",
		inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("failed to register in-process Codex task")
	}
	result := make(chan string, 1)
	go func() {
		text, _ := h.interruptExternalCodexTask(externalCodexTaskCommand{
			ctx: context.Background(), key: "conversation-1", agent: ag, actor: "user-1",
		})
		result <- text
	}()
	select {
	case <-ag.threadStateEntered:
	case <-time.After(taskWaitTimeout):
		t.Fatal("runtime state read did not start")
	}
	if !task.claimTerminal() {
		t.Fatal("failed to claim concurrent terminal")
	}
	close(threadStateRelease)
	select {
	case text := <-result:
		if !strings.Contains(text, "已经结束") {
			t.Fatalf("text=%q, want terminal result", text)
		}
	case <-time.After(taskWaitTimeout):
		t.Fatal("/stop did not finish after terminal")
	}
	if ag.interruptCalls != 0 {
		t.Fatalf("interrupt calls=%d, terminal task must not be interrupted", ag.interruptCalls)
	}
	if task.runtimeOwner != agent.CodexRuntimeUnknown {
		t.Fatalf("runtime=%s, terminal task metadata must not be refreshed", task.runtimeOwner)
	}
}

func TestCodexStopRuntimeProbeTimeoutKeepsTaskControllable(t *testing.T) {
	h := NewHandler(nil, nil)
	h.codexControlTimeout = 20 * time.Millisecond
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	ag.inspectRelease = make(chan struct{})
	task, _, _ := h.beginActiveTask(context.Background(), "conversation-1", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "codex",
		runtimeOwner:  agent.CodexRuntimeWeClaw,
		codexThreadID: "thread-active", codexTurnID: "turn-active",
	})

	result := make(chan struct {
		text    string
		handled bool
	}, 1)
	go func() {
		text, handled := h.interruptExternalCodexTask(externalCodexTaskCommand{
			ctx: context.Background(), key: "conversation-1", agent: ag, actor: "user-1",
		})
		result <- struct {
			text    string
			handled bool
		}{text: text, handled: handled}
	}()

	select {
	case got := <-result:
		if !got.handled || !strings.Contains(got.text, "无法确认 Codex 实时运行位置") {
			t.Fatalf("handled=%v text=%q", got.handled, got.text)
		}
	case <-time.After(taskWaitTimeout):
		t.Fatal("/stop did not return after the internal control timeout")
	}
	if taskPhase(task) != codexTaskRunning || task.stopRequested {
		t.Fatalf("phase=%s stopRequested=%v", taskPhase(task), task.stopRequested)
	}
	assertCodexThreadLockReusable(t, h, "thread-active")
}

func TestCodexReservedExternalTaskRejectsGuideAndStopBeforeRuntimeCall(t *testing.T) {
	h := NewHandler(nil, nil)
	state := agent.CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1"}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	h.defaultName = "codex"
	h.agents["codex"] = ag
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer h.cancelExternalCodexTaskReservation(reservation)
	h.storePendingGuide(opts.conversationID, pendingAgentTask{message: "补充要求", run: func() {}})
	guide, guideHandled := h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{
		ctx: context.Background(), key: opts.conversationID, agentName: "codex", actor: opts.actorUserID,
	})
	stop, stopHandled := h.interruptExternalCodexTask(externalCodexTaskCommand{
		ctx: context.Background(), key: opts.conversationID, agent: ag, actor: opts.actorUserID,
	})
	if !guideHandled || !stopHandled || !strings.Contains(guide, "观察尚未激活") || !strings.Contains(stop, "观察尚未激活") {
		t.Fatalf("guide=(%v,%q) stop=(%v,%q)", guideHandled, guide, stopHandled, stop)
	}
	if ag.bindCalls != 0 || ag.steerThreadID != "" || ag.interruptCalls != 0 {
		t.Fatalf("reserved 阶段调用了 runtime：inspect=%d steer=%q interrupt=%d", ag.bindCalls, ag.steerThreadID, ag.interruptCalls)
	}
}

func TestCodexSelectedSharedTaskImmediatelyAcceptsGuideAndStop(t *testing.T) {
	fixture := newCodexSessionBindingFixture(t)
	fixture.setActiveTarget("turn-b")
	fixture.ag.watchDone = make(chan struct{})
	fixture.h.defaultName = "codex"
	fixture.h.agents["codex"] = fixture.ag
	request := fixture.request("thread-b")
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil {
		t.Fatal(err)
	}
	defer close(fixture.ag.watchDone)
	key := result.route.conversationID
	fixture.h.storePendingGuide(key, pendingAgentTask{message: "补充要求", run: func() {}})

	guide, guideHandled := fixture.h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{
		ctx: context.Background(), key: key, agentName: "codex", actor: request.actorUserID,
	})
	stop, stopHandled := fixture.h.interruptExternalCodexTask(externalCodexTaskCommand{
		ctx: context.Background(), key: key, agent: fixture.ag, actor: request.actorUserID,
	})

	if !guideHandled || !strings.Contains(guide, "已发送") ||
		!stopHandled || !strings.Contains(stop, "等待任务结束") {
		t.Fatalf("guide=(%v,%q) stop=(%v,%q)", guideHandled, guide, stopHandled, stop)
	}
	if fixture.ag.steerThreadID != "thread-b" || fixture.ag.steerTurnID != "turn-b" ||
		fixture.ag.interruptThreadID != "thread-b" || fixture.ag.interruptTurnID != "turn-b" {
		t.Fatalf("steer=(%q,%q) interrupt=(%q,%q)", fixture.ag.steerThreadID,
			fixture.ag.steerTurnID, fixture.ag.interruptThreadID, fixture.ag.interruptTurnID)
	}
	wantSteer, wantInterrupt := fixture.ag.steerThreadID, fixture.ag.interruptCalls
	fixture.h.storePendingGuide(key, pendingAgentTask{message: "越权要求", run: func() {}})
	unauthorizedGuide, _ := fixture.h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{
		ctx: context.Background(), key: key, agentName: "codex", actor: "other-actor",
	})
	unauthorizedStop, _ := fixture.h.interruptExternalCodexTask(externalCodexTaskCommand{
		ctx: context.Background(), key: key, agent: fixture.ag, actor: "other-actor",
	})
	if !strings.Contains(unauthorizedGuide, "只有任务发起人") ||
		!strings.Contains(unauthorizedStop, "只有任务发起人") {
		t.Fatalf("guide=%q stop=%q", unauthorizedGuide, unauthorizedStop)
	}
	if fixture.ag.steerThreadID != wantSteer || fixture.ag.interruptCalls != wantInterrupt {
		t.Fatal("非任务发起人不应调用共享 host active turn")
	}
}
