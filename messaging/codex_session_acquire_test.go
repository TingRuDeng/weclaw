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

func TestAcquireCodexSessionCompensatesTargetWhenOldReleaseFails(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffErrors["thread-a"] = errors.New("释放失败")
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err == nil {
		t.Fatal("旧 thread 释放失败时事务不应成功")
	}
	assertCodexAcquireOriginalState(t, fixture, 3)
	requests := fixture.agent.handoffRequests()
	if len(requests) < 3 || requests[len(requests)-1].Intent.Owner != agent.CodexControlDesktop {
		t.Fatalf("handoff history=%#v，最后应恢复 B 原 desktop intent", requests)
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
}
