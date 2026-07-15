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

func TestCodexOwnerStatusReturnsCardState(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	result, handled := h.dispatchCodexUtilityCommand(runtime)
	if !handled || !result.ShowCard || !strings.Contains(result.Reply, "控制方: 未认领") {
		t.Fatalf("handled=%v result=%#v", handled, result)
	}
	if ag.bindCalls != 1 {
		t.Fatalf("探测次数=%d，期望 1", ag.bindCalls)
	}
}

func TestCodexOwnerStatusTimeoutReleasesThreadLock(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	ag.inspectRelease = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runtime.ctx = ctx

	result := h.handleCodexOwnerCommand(runtime)
	if !strings.Contains(result.Reply, "控制权查询超时") ||
		!strings.Contains(result.Reply, "运行位置未确认") {
		t.Fatalf("reply=%q", result.Reply)
	}
	assertCodexThreadLockReusable(t, h, "thread-1")
}

func TestCodexOwnerRemoteCommitsAfterSuccessfulHandoff(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	runtime.fields = []string{"/cx", "owner", "remote"}
	result, handled := h.dispatchCodexUtilityCommand(runtime)
	intent := h.codexSessions.controlIntent("thread-1")
	if !handled || !strings.Contains(result.Reply, "已移交给当前远程窗口") {
		t.Fatalf("handled=%v result=%#v", handled, result)
	}
	if intent.Owner != codexControlRemote || intent.RouteBindingKey != runtime.bindingKey ||
		intent.ConversationID != runtime.codexRoute("").conversationID || intent.Revision != 1 {
		t.Fatalf("intent=%#v", intent)
	}
	if ag.handoffCalls != 1 || ag.lastRuntimeReq.Intent.Revision != 1 {
		t.Fatalf("handoff=%d request=%#v", ag.handoffCalls, ag.lastRuntimeReq)
	}
}

func TestCodexOwnerRemoteFailureDoesNotPersistIntent(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	ag.handoffErr = errors.New("探测失败")
	runtime.fields = []string{"/cx", "owner", "remote"}
	result, _ := h.dispatchCodexUtilityCommand(runtime)
	intent := h.codexSessions.controlIntent("thread-1")
	if intent.Owner != codexControlUnclaimed || intent.Revision != 0 {
		t.Fatalf("失败后不应写入控制意图: %#v", intent)
	}
	if !strings.Contains(result.Reply, "移交失败") {
		t.Fatalf("reply=%q", result.Reply)
	}
}

func TestCodexOwnerHandoffTimeoutDoesNotPersistIntent(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	ag.handoffRelease = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runtime.ctx = ctx
	runtime.fields = []string{"/cx", "owner", "remote"}

	result, _ := h.dispatchCodexUtilityCommand(runtime)
	intent := h.codexSessions.controlIntent("thread-1")
	if intent.Owner != codexControlUnclaimed || intent.Revision != 0 {
		t.Fatalf("超时后不应写入控制意图: %#v", intent)
	}
	if !strings.Contains(result.Reply, "移交结果未确认") ||
		!strings.Contains(result.Reply, "控制意图未提交") || !strings.Contains(result.Reply, "重新查询") {
		t.Fatalf("reply=%q", result.Reply)
	}
	assertCodexThreadLockReusable(t, h, "thread-1")
}

func TestCodexOwnerThreadLockTimeoutDoesNotExecuteHandoff(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	h.codexLockWaitTimeout = 20 * time.Millisecond
	runtime.fields = []string{"/cx", "owner", "remote"}
	unlockHolder := h.lockCodexThreadControl("thread-1")
	resultCh := make(chan navigationCommandResult, 1)
	go func() {
		resultCh <- h.handleCodexOwnerCommand(runtime)
	}()

	select {
	case result := <-resultCh:
		unlockHolder()
		if !strings.Contains(result.Reply, "前一项会话操作仍在处理") ||
			!strings.Contains(result.Reply, "移交未执行") {
			t.Fatalf("reply=%q", result.Reply)
		}
	case <-time.After(500 * time.Millisecond):
		unlockHolder()
		t.Fatal("owner thread 锁等待未按时结束")
	}
	if ag.handoffCalls != 0 {
		t.Fatalf("handoff=%d，锁超时后不应执行移交", ag.handoffCalls)
	}
}

func assertCodexThreadLockReusable(t *testing.T, h *Handler, threadID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	unlock, err := h.lockCodexThreadControlContext(ctx, threadID)
	if err != nil {
		t.Fatalf("thread 控制锁无法复用: %v", err)
	}
	unlock()
}

func TestCodexOwnerPersistenceFailureDoesNotReportSuccess(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	h.codexSessions.SetFilePath(t.TempDir())
	ag.bindErr = errors.New("重新探测失败")
	runtime.fields = []string{"/cx", "owner", "remote"}

	result, _ := h.dispatchCodexUtilityCommand(runtime)

	if !strings.Contains(result.Reply, "控制权提交失败") ||
		!strings.Contains(result.Reply, "运行时回滚失败") || strings.Contains(result.Reply, "已移交给") {
		t.Fatalf("reply=%q", result.Reply)
	}
	intent := h.codexSessions.controlIntent("thread-1")
	if intent.Owner != codexControlUnclaimed || intent.Revision != 0 {
		t.Fatalf("intent=%#v，want rolled back", intent)
	}
	if ag.handoffCalls != 1 || ag.bindCalls != 1 {
		t.Fatalf("handoff=%d resync=%d", ag.handoffCalls, ag.bindCalls)
	}
}

func TestCodexOwnerDesktopRejectsActiveRemoteTask(t *testing.T) {
	h, _, runtime := codexOwnerCommandFixture(t)
	route := runtime.codexRoute("thread-1")
	_, err := h.codexSessions.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-1", Owner: codexControlRemote,
		RouteBindingKey: runtime.bindingKey, ConversationID: route.conversationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _, started := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{owner: runtime.actorUserID})
	if !started {
		t.Fatal("创建测试任务失败")
	}
	defer h.finishActiveTask(route.conversationID, task)
	runtime.fields = []string{"/cx", "owner", "desktop"}
	result, _ := h.dispatchCodexUtilityCommand(runtime)
	if !strings.Contains(result.Reply, "当前远程任务结束") {
		t.Fatalf("reply=%q", result.Reply)
	}
	if intent := h.codexSessions.controlIntent("thread-1"); intent.Owner != codexControlRemote {
		t.Fatalf("活动任务期间不应归还控制权: %#v", intent)
	}
}

func TestCodexOwnerRemoteRejectsTaskObservedByAnotherRoute(t *testing.T) {
	h, _, runtime := codexOwnerCommandFixture(t)
	task, _, started := h.beginActiveTask(context.Background(), "other-conversation", activeTaskMeta{
		owner: "other-user", codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	if !started {
		t.Fatal("创建其他窗口测试任务失败")
	}
	defer h.finishActiveTask("other-conversation", task)
	runtime.fields = []string{"/cx", "owner", "remote"}

	result, _ := h.dispatchCodexUtilityCommand(runtime)

	if !strings.Contains(result.Reply, "另一个消息窗口") {
		t.Fatalf("reply=%q", result.Reply)
	}
	if intent := h.codexSessions.controlIntent("thread-1"); intent.Owner != codexControlUnclaimed {
		t.Fatalf("intent=%#v，活动任务期间不应跨窗口接管", intent)
	}
}

func TestCodexOwnerHandoffWaitsForTaskAdmission(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspace})
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{ThreadID: "thread-1"})
	routeA := codexOwnerTestRoute("route-a", workspace)
	routeB := codexOwnerTestRoute("route-b", workspace)
	h.codexSessions.setThread(routeA.bindingKey, workspace, routeA.threadID)
	h.codexSessions.setThread(routeB.bindingKey, workspace, routeB.threadID)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: "route-a", agentName: "codex", bindingKey: routeA.bindingKey,
		workspace: workspace, threadID: routeA.threadID,
	})
	inspectRelease := make(chan struct{})
	turnRelease := make(chan struct{})
	ag.inspectEntered = make(chan struct{}, 1)
	ag.inspectRelease = inspectRelease
	ag.turnEntered = make(chan struct{}, 1)
	ag.turnRelease = turnRelease
	defer close(turnRelease)
	progressCfg := config.DefaultProgressConfig()
	progressCfg.Mode = progressModeOff
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	go h.startCodexAgentTask(codexAgentTaskOptions{
		ctx: context.Background(), platform: platform.PlatformWeChat,
		userID: "user-a", routeUserID: "route-a", reply: reply, agentName: "codex",
		message: "执行任务", agent: ag, progressCfg: progressCfg, route: routeA,
	})
	waitDone(t, ag.inspectEntered, "任务预检")
	resultCh := make(chan navigationCommandResult, 1)
	go func() {
		resultCh <- h.handleCodexOwnerCommand(codexSessionCommandRuntime{
			ctx: context.Background(), actorUserID: "user-b", routeUserID: "route-b",
			fields: []string{"/cx", "owner", "remote"}, agentName: "codex", agent: ag,
			bindingKey: routeB.bindingKey, workspaceRoot: workspace,
		})
	}()
	close(inspectRelease)
	waitDone(t, ag.turnEntered, "Codex turn 启动")

	var result navigationCommandResult
	select {
	case result = <-resultCh:
	case <-time.After(taskWaitTimeout):
		t.Fatal("未等到控制权移交结果")
	}
	if !strings.Contains(result.Reply, "另一个消息窗口") {
		t.Fatalf("reply=%q，任务准入后不应被其他窗口接管", result.Reply)
	}
	intent := h.codexSessions.controlIntent("thread-1")
	if intent.RouteBindingKey != routeA.bindingKey {
		t.Fatalf("intent=%#v，控制权不应在任务准入期间变化", intent)
	}
}

func codexOwnerTestRoute(routeUserID string, workspace string) codexConversationRoute {
	return codexConversationRoute{
		bindingKey: codexBindingKey(routeUserID, "codex"), workspaceRoot: workspace,
		conversationID: buildCodexConversationID(routeUserID, "codex", workspace), threadID: "thread-1",
	}
}

func TestFeishuCodexOwnerStatusUsesChoiceCard(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	msg := platform.IncomingMessage{Platform: platform.PlatformFeishu, UserID: "user-1"}
	result := cardNavigationResult("Codex 会话控制")
	if !h.sendFeishuCodexOwnerChoices(feishuCodexSessionCommandRequest{
		ctx: context.Background(), message: msg, routeUserID: "route-1",
		reply: reply, trimmed: "/cx owner", result: result,
	}) {
		t.Fatal("未发送所有权选择卡片")
	}
	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) != 2 {
		t.Fatalf("choices=%#v", reply.Choices)
	}
	if reply.Choices[0].Choices[0].ID != "/cx owner remote" ||
		reply.Choices[0].Choices[1].ID != "/cx owner desktop" {
		t.Fatalf("choices=%#v", reply.Choices[0].Choices)
	}
}

func TestFeishuCodexOwnerActionDoesNotCreateAnotherChoiceCard(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	request := feishuCodexSessionCommandRequest{
		ctx:         context.Background(),
		message:     platform.IncomingMessage{Platform: platform.PlatformFeishu, UserID: "user-1"},
		routeUserID: "route-1", reply: reply,
		trimmed: "/cx owner remote", result: cardNavigationResult("已移交"),
	}
	if h.sendFeishuCodexOwnerChoices(request) {
		t.Fatal("控制权动作完成后不应再次生成选择卡")
	}
}

func codexOwnerCommandFixture(t *testing.T) (*Handler, *fakeCodexLiveAgent, codexSessionCommandRuntime) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{ThreadID: "thread-1"})
	bindingKey := codexBindingKey("route-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-1")
	runtime := codexSessionCommandRuntime{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		fields: []string{"/cx", "owner"}, agentName: "codex", agent: ag,
		bindingKey: bindingKey, workspaceRoot: workspace,
	}
	return h, ag, runtime
}
