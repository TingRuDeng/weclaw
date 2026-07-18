package messaging

import (
	"errors"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestAcquireCodexSessionSwitchesAtoBAndReleasesA(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil {
		t.Fatal(err)
	}
	if result.route.threadID != "thread-b" || result.resolution.Request.Intent.Owner != agent.CodexControlRemote {
		t.Fatalf("result=%#v", result)
	}
	if got := fixture.h.ensureCodexSessions().controlIntent("thread-a"); got.Owner != codexControlDesktop {
		t.Fatalf("thread-a=%#v", got)
	}
	if got := fixture.h.ensureCodexSessions().controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("thread-b=%#v", got)
	}
}

func TestAcquireCodexSessionAbandonsOldExternalObservation(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	conversationID, taskCtx, task := fixture.startExternalObservation("thread-a", fixture.workspaceA, "turn-a")
	defer fixture.h.finishActiveTask(conversationID, task)

	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	if _, active := fixture.h.activeTask(conversationID); active {
		t.Fatal("切换到新会话后不应继续占用旧外部观察任务")
	}
	select {
	case <-taskCtx.Done():
	default:
		t.Fatal("旧外部观察任务应被取消")
	}
	if task.shouldSendFinal() {
		t.Fatal("放弃观察后不应继续向旧会话发送最终结果")
	}
	if got := fixture.h.ensureCodexSessions().controlIntent("thread-a"); got.Owner != codexControlDesktop {
		t.Fatalf("旧 thread 应归还 Desktop: %#v", got)
	}
	if got := fixture.h.ensureCodexSessions().controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("目标 thread 应切到 remote: %#v", got)
	}
}

func TestAcquireCodexSessionDetachesOldInProcessTaskWithoutCancelingTurn(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	conversationID, taskCtx, task := fixture.startInProcessCodexTask("thread-a", fixture.workspaceA)
	defer fixture.h.finishActiveTask(conversationID, task)

	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	if _, active := fixture.h.activeTask(conversationID); active {
		t.Fatal("切换到新会话后不应继续占用旧本进程任务槽位")
	}
	select {
	case <-taskCtx.Done():
		t.Fatal("本进程 Codex turn 不应被切换操作取消")
	default:
	}
	if task.shouldSendFinal() {
		t.Fatal("切走后旧任务不应再向当前窗口发送最终结果")
	}
}

func TestAcquireCodexSessionRejectsOtherRemoteWithoutChanges(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	current := fixture.h.ensureCodexSessions().controlIntent("thread-b")
	_, err := fixture.h.ensureCodexSessions().updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-b", Owner: codexControlRemote,
		RouteBindingKey: "other-route", ConversationID: "other-conversation",
		ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := fixture.snapshot()
	_, err = fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if !errors.Is(err, errCodexRemoteSelectionOtherRoute) {
		t.Fatalf("error=%v", err)
	}
	assertCodexAcquireState(t, fixture, want)
}

func TestAcquireCodexSessionKeepsTargetWhenOldReleaseFails(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffErrors["thread-a"] = errors.New("释放失败")
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	if got := fixture.h.ensureCodexSessions().controlIntent("thread-a"); got.Owner != codexControlDesktop {
		t.Fatalf("旧 thread 的持久化所有权应已释放: %#v", got)
	}
	if got := fixture.h.ensureCodexSessions().controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("目标 thread 应保持远程所有权: %#v", got)
	}
	requests := fixture.agent.handoffRequests()
	if len(requests) != 2 || requests[0].Ref.ThreadID != "thread-b" || requests[1].Ref.ThreadID != "thread-a" {
		t.Fatalf("handoff history=%#v", requests)
	}
}

func TestAcquireCodexSessionNormalizesMultipleRouteOwnedThreads(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	workspaceC := "/workspace/c"
	fixture.h.codexSessions.setThread(fixture.bindingKey, workspaceC, "thread-c")
	claimRemoteControlForTest(t, fixture.h, fakeRemoteControlOptions{
		routeUserID: fixture.routeUser, agentName: "codex", bindingKey: fixture.bindingKey,
		workspace: workspaceC, threadID: "thread-c",
	})
	fixture.agent.setThreadBinding("thread-c", desktopAcquireBinding("thread-c"))
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil {
		t.Fatal(err)
	}
	for _, threadID := range []string{"thread-a", "thread-c"} {
		if got := fixture.h.codexSessions.controlIntent(threadID); got.Owner != codexControlDesktop {
			t.Fatalf("%s intent=%#v", threadID, got)
		}
	}
	requests := fixture.agent.handoffRequests()
	if len(requests) != 3 || requests[0].Ref.ThreadID != "thread-b" ||
		requests[1].Ref.ThreadID != "thread-a" || requests[2].Ref.ThreadID != "thread-c" {
		t.Fatalf("handoff order=%#v", requests)
	}
}

func TestAcquireCodexSessionIsIdempotent(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	if _, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b")); err != nil {
		t.Fatal(err)
	}
	want := fixture.snapshot()
	if _, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b")); err != nil {
		t.Fatal(err)
	}
	assertCodexAcquireState(t, fixture, want)
}

func TestAcquireCodexSessionActiveDesktopStartsObserver(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.setActiveTarget("turn-b")
	fixture.agent.watchDone = make(chan struct{})
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.externalActive || !result.externalState.Controllable {
		t.Fatalf("result=%#v", result)
	}
	if _, active := fixture.h.activeTask(result.route.conversationID); !active {
		t.Fatal("成功提交前应预留并在提交后激活观察器")
	}
	close(fixture.agent.watchDone)
	waitUntil(t, func() bool {
		_, active := fixture.h.activeTask(result.route.conversationID)
		return !active
	})
	binding := fixture.agent.threadBinding("thread-b")
	if binding.State.Active || binding.State.ActiveTurnID != "" || binding.State.LastTurnID != "turn-b" {
		t.Fatalf("terminal binding=%#v，观察任务完成后应同步运行态", binding)
	}
}

func TestAcquireCodexSessionAdoptsActiveTurnFoundAfterHandoff(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.setThreadBinding("thread-b", desktopAcquireBinding("thread-b"))
	fixture.agent.threadState = agent.CodexThreadState{
		ThreadID: "thread-b", Active: true, ActiveTurnID: "turn-b",
	}
	fixture.agent.watchDone = make(chan struct{})

	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.externalActive {
		t.Fatalf("result=%#v，接管后二次发现的活动 turn 应启动观察", result)
	}
	binding := fixture.agent.threadBinding("thread-b")
	if !binding.State.Active || binding.State.ActiveTurnID != "turn-b" {
		t.Fatalf("binding=%#v，观察到的活动 turn 未同步到运行态", binding)
	}
	close(fixture.agent.watchDone)
	waitUntil(t, func() bool {
		_, active := fixture.h.activeTask(result.route.conversationID)
		return !active
	})
}
