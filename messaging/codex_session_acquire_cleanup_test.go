package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestAcquireCodexSessionDeadlineCleanupCompensatesEarlierHandoff(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	request := fixture.request("thread-b")
	request.ctx = ctx
	fixture.agent.rejectCanceledContext = true
	fixture.agent.handoffReleases["thread-a"] = make(chan struct{})
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("error=%v", err)
	}
	assertCodexAcquireOriginalState(t, fixture, 3)
	assertFakeCodexBindingOwner(t, fixture.agent, "thread-b", agent.CodexControlDesktop)
}

func TestAcquireCodexSessionCanceledCleanupCompensatesEarlierHandoff(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	request := fixture.request("thread-b")
	request.ctx = ctx
	fixture.agent.rejectCanceledContext = true
	fixture.agent.handoffReleases["thread-a"] = make(chan struct{})
	fixture.agent.handoffHooks["thread-a"] = cancel
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if !errors.Is(err, context.Canceled) || errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("error=%v", err)
	}
	assertCodexAcquireOriginalState(t, fixture, 3)
	assertFakeCodexBindingOwner(t, fixture.agent, "thread-b", agent.CodexControlDesktop)
}

func TestAcquireCodexSessionNonTimeoutAfterSideEffectIsCalibrated(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffAfterErrors["thread-a"] = errors.New("checkpoint 读取失败")
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err == nil || errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("error=%v", err)
	}
	if fixture.agent.bindCalls != 1 {
		t.Fatalf("inspect count=%d, want 1", fixture.agent.bindCalls)
	}
	assertCodexAcquireOriginalState(t, fixture, 3)
	assertFakeCodexBindingOwner(t, fixture.agent, "thread-a", agent.CodexControlRemote)
	assertFakeCodexBindingOwner(t, fixture.agent, "thread-b", agent.CodexControlDesktop)
}

func TestAcquireCodexSessionCalibrationFailureMarksConflict(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffAfterErrors["thread-b"] = errors.New("恢复后读取失败")
	fixture.agent.inspectErrors["thread-b"] = errors.New("校准失败")
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if !errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("error=%v", err)
	}
	binding := fixture.agent.threadBinding("thread-b")
	if binding.Runtime != agent.CodexRuntimeConflict || binding.ConflictReason == "" {
		t.Fatalf("binding=%#v", binding)
	}
}

func TestAcquireCodexSessionCompensationFailureMarksTouchedThreadConflict(t *testing.T) {
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
	binding := fixture.agent.threadBinding("thread-b")
	if binding.Runtime != agent.CodexRuntimeConflict {
		t.Fatalf("binding=%#v", binding)
	}
}

func TestAcquireCodexSessionExplicitHandoffRecoversMarkedConflict(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	request := fixture.request("thread-b")
	if err := fixture.agent.MarkCodexRuntimeConflict(context.Background(), agent.CodexRuntimeRequest{
		Ref:    request.route.ref("thread-b"),
		Intent: agent.CodexControlIntent{Owner: agent.CodexControlDesktop, Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.resolution.Binding.Runtime == agent.CodexRuntimeConflict ||
		result.resolution.Binding.ConflictReason != "" {
		t.Fatalf("binding=%#v", result.resolution.Binding)
	}
}

func TestCodexSessionAcquireCleanupContextKeepsValuesWithoutCancellation(t *testing.T) {
	type cleanupContextKey struct{}
	parent, cancelParent := context.WithCancel(context.WithValue(
		context.Background(), cleanupContextKey{}, "trace-value",
	))
	cancelParent()
	cleanup, cancelCleanup := newCodexSessionAcquireCleanupContext(parent)
	defer cancelCleanup()
	if cleanup.Err() != nil || cleanup.Value(cleanupContextKey{}) != "trace-value" {
		t.Fatalf("cleanup err=%v value=%v", cleanup.Err(), cleanup.Value(cleanupContextKey{}))
	}
	deadline, ok := cleanup.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > codexSessionAcquireCleanupTimeout {
		t.Fatalf("cleanup deadline=%v ok=%v", deadline, ok)
	}
}

func assertFakeCodexBindingOwner(t *testing.T, ag *fakeCodexLiveAgent, threadID string, owner agent.CodexControlOwner) {
	t.Helper()
	if binding := ag.threadBinding(threadID); binding.Control.Owner != owner {
		t.Fatalf("thread=%s binding=%#v, want owner=%s", threadID, binding, owner)
	}
}
