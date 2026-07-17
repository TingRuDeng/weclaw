package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
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

func TestAcquireCodexSessionPersistenceFailureSkipsRuntime(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.h.codexSessions.SetFilePath(t.TempDir() + "/codex-sessions.json")
	fixture.h.codexSessions.writeState = func(string, []byte) error { return errors.New("写盘失败") }
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err == nil {
		t.Fatal("持久化失败时事务不应成功")
	}
	assertCodexAcquireOriginalState(t, fixture, 0)
	if got := len(fixture.agent.handoffRequests()); got != 0 {
		t.Fatalf("handoff count=%d, 所有权落盘失败前不得触碰 runtime", got)
	}
}

func TestAcquireCodexSessionAgentSelectionFailureRollsBackBeforeRuntime(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	statePath := filepath.Join(t.TempDir(), "agent-sessions.json")
	if err := fixture.h.SetAgentSessionFile(statePath); err != nil {
		t.Fatal(err)
	}
	if err := fixture.h.ensureAgentSessions().Set(fixture.routeUser, "claude"); err != nil {
		t.Fatal(err)
	}
	invalidParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.h.ensureAgentSessions().filePath = filepath.Join(invalidParent, "state.json")
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err == nil {
		t.Fatal("Agent 选择持久化失败时应拒绝切换")
	}
	assertCodexAcquireOriginalState(t, fixture, 0)
	if selected, ok := fixture.h.ensureAgentSessions().Get(fixture.routeUser); !ok || selected != "claude" {
		t.Fatalf("selected=%q ok=%t", selected, ok)
	}
}

func TestAcquireCodexSessionHandoffTimeoutDoesNotRetrySideEffect(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffErrors["thread-b"] = context.DeadlineExceeded
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	if got := len(fixture.agent.handoffRequests()); got != 2 {
		t.Fatalf("handoff count=%d, 每个 target/release 最多各调用一次", got)
	}
	if fixture.agent.bindCalls != 0 {
		t.Fatalf("inspect count=%d, handoff 失败后不得二次探测", fixture.agent.bindCalls)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("target owner=%#v", got)
	}
	if got := fixture.agent.threadBinding("thread-b"); got.Runtime == agent.CodexRuntimeConflict {
		t.Fatalf("handoff 超时不能伪造冲突，runtime=%#v", got)
	}
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

func TestAcquireCodexSessionIdempotentDesktopTargetReusesFollowerRuntime(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	setAcquireTargetRemoteForCurrentRoute(t, fixture)
	fixture.agent.inspectErrors["thread-b"] = errors.New("校准失败")
	targetHandoffCount := func() int {
		count := 0
		for _, request := range fixture.agent.handoffRequests() {
			if request.Ref.ThreadID == "thread-b" {
				count++
			}
		}
		return count
	}
	beforeHandoffs := targetHandoffCount()
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil || result.runtimeErr != nil || result.resolution.Binding.Runtime != agent.CodexRuntimeDesktop {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	if fixture.agent.bindCalls != 0 {
		t.Fatalf("inspect count=%d, 幂等选择只应读取 CurrentCodexRuntime", fixture.agent.bindCalls)
	}
	if got := targetHandoffCount(); got != beforeHandoffs {
		t.Fatalf("handoff count=%d，Desktop follower runtime 已可写时不应重做 handoff", got-beforeHandoffs)
	}
}

func TestAcquireCodexSessionIdempotentUnknownTargetForcesRecovery(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	setAcquireTargetRemoteForCurrentRoute(t, fixture)
	fixture.agent.setThreadBinding("thread-b", agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeUnknown,
		State:   agent.CodexThreadState{ThreadID: "thread-b"},
	})

	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))

	if err != nil || result.runtimeErr != nil || result.resolution.Binding.Runtime != agent.CodexRuntimeWeClaw {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	targetHandoffs := 0
	for _, request := range fixture.agent.handoffRequests() {
		if request.Ref.ThreadID == "thread-b" {
			targetHandoffs++
		}
	}
	if targetHandoffs != 1 {
		t.Fatalf("target handoff count=%d，显式选择应恢复 unknown runtime", targetHandoffs)
	}
}

func TestAcquireCodexSessionReservationConflictKeepsOwnerAndBlocksRuntime(t *testing.T) {
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
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || !errors.Is(result.runtimeErr, errExternalCodexTaskReservationConflict) {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("target owner=%#v", got)
	}
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
	assertCodexAcquireOriginalState(t, fixture, 0)
}

func TestRenderCodexSessionAcquireFailureHidesOtherRouteIdentity(t *testing.T) {
	err := errors.Join(errCodexRemoteSelectionOtherRoute, errors.New("route-user-secret"))
	if got := renderCodexSessionAcquireFailure(err); got != "其他远程窗口正在控制该会话，请原窗口先释放。" {
		t.Fatalf("message=%q", got)
	}
}
