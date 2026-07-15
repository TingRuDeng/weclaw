package messaging

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireCodexSessionRejectsActiveOldRemoteTask(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	conversation := buildCodexConversationID(fixture.routeUser, "codex", fixture.workspaceA)
	task, _, started := fixture.h.beginActiveTask(context.Background(), conversation, activeTaskMeta{
		owner: fixture.routeUser, routeUserID: fixture.routeUser, agentName: "codex",
		codexThreadID: "thread-a", codexTurnID: "turn-a",
	})
	if !started {
		t.Fatal("未能建立活动旧任务")
	}
	defer fixture.h.finishActiveTask(conversation, task)
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if !errors.Is(err, errCodexSessionAcquireActiveOld) {
		t.Fatalf("error=%v", err)
	}
	assertCodexAcquireOriginalState(t, fixture, 0)
}

func TestAcquireCodexSessionPersistenceFailureCompensatesRuntime(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.h.codexSessions.SetFilePath(t.TempDir() + "/codex-sessions.json")
	fixture.h.codexSessions.writeState = func(string, []byte) error { return errors.New("写盘失败") }
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err == nil {
		t.Fatal("持久化失败时事务不应成功")
	}
	assertCodexAcquireOriginalState(t, fixture, 4)
	if got := len(fixture.agent.handoffRequests()); got != 4 {
		t.Fatalf("handoff count=%d, want target/release/reverse release/reverse target", got)
	}
}

func TestAcquireCodexSessionCompensationFailureIsFailClosed(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.h.codexSessions.SetFilePath(t.TempDir() + "/codex-sessions.json")
	fixture.h.codexSessions.writeState = func(string, []byte) error {
		fixture.agent.mu.Lock()
		fixture.agent.handoffErrors["thread-b"] = errors.New("恢复失败")
		fixture.agent.mu.Unlock()
		return errors.New("写盘失败")
	}
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if !errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("error=%v", err)
	}
	assertCodexAcquireOriginalState(t, fixture, 4)
}

func TestAcquireCodexSessionHandoffTimeoutDoesNotRetrySideEffect(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffErrors["thread-b"] = context.DeadlineExceeded
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if got := len(fixture.agent.handoffRequests()); got != 1 {
		t.Fatalf("handoff count=%d, timeout 后不得重试副作用", got)
	}
	if fixture.agent.bindCalls != 1 {
		t.Fatalf("inspect count=%d, want 1", fixture.agent.bindCalls)
	}
	assertCodexAcquireOriginalState(t, fixture, 1)
}

func TestAcquireCodexSessionLockTimeoutKeepsOriginalState(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.h.codexLockWaitTimeout = 20 * time.Millisecond
	unlock := fixture.h.lockCodexThreadControl("thread-a")
	defer unlock()
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if !isCodexSessionControlTimeout(err) {
		t.Fatalf("error=%v", err)
	}
	assertCodexAcquireOriginalState(t, fixture, 0)
}

func TestAcquireCodexSessionHandoffTimeoutInspectFailureIsUncertain(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffErrors["thread-b"] = context.DeadlineExceeded
	fixture.agent.inspectErrors["thread-b"] = errors.New("校准失败")
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if !errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("error=%v", err)
	}
	assertCodexAcquireOriginalState(t, fixture, 1)
}

func TestAcquireCodexSessionReservationConflictCompensatesRuntime(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.setActiveTarget("turn-b")
	request := fixture.request("thread-b")
	task, _, started := fixture.h.beginActiveTask(context.Background(), request.route.conversationID, activeTaskMeta{
		owner: "other-user", routeUserID: "other-route", agentName: "codex",
		codexThreadID: "thread-b", codexTurnID: "other-turn",
	})
	if !started {
		t.Fatal("未能建立冲突观察任务")
	}
	defer fixture.h.finishActiveTask(request.route.conversationID, task)
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if !errors.Is(err, errExternalCodexTaskReservationConflict) {
		t.Fatalf("error=%v", err)
	}
	assertCodexAcquireOriginalState(t, fixture, 4)
}

func TestAcquireCodexSessionPersistenceFailureCancelsObserverReservation(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.setActiveTarget("turn-b")
	fixture.h.codexSessions.SetFilePath(t.TempDir() + "/codex-sessions.json")
	fixture.h.codexSessions.writeState = func(string, []byte) error { return errors.New("写盘失败") }
	request := fixture.request("thread-b")
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if err == nil {
		t.Fatal("持久化失败时事务不应成功")
	}
	if _, active := fixture.h.activeTask(request.route.conversationID); active {
		t.Fatal("提交失败后不应残留未启动的观察 reservation")
	}
	assertCodexAcquireOriginalState(t, fixture, 4)
}

func TestRenderCodexSessionAcquireFailureHidesOtherRouteIdentity(t *testing.T) {
	err := errors.Join(errCodexRemoteSelectionOtherRoute, errors.New("route-user-secret"))
	if got := renderCodexSessionAcquireFailure(err); got != "其他远程窗口正在控制，请原窗口先释放。" {
		t.Fatalf("message=%q", got)
	}
}
