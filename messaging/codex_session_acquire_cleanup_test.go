package messaging

import (
	"context"
	"errors"
	"sync"
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

type recordedRuntimeContext struct {
	operation string
	threadID  string
	deadline  time.Time
}

func TestRollbackCompensationsShareOneCleanupDeadline(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.h.codexSessions.SetFilePath(t.TempDir() + "/codex-sessions.json")
	var recordsMu sync.Mutex
	recording := false
	records := make([]recordedRuntimeContext, 0, 8)
	fixture.agent.recordRuntimeContext = func(operation string, ctx context.Context, req agent.CodexRuntimeRequest) {
		recordsMu.Lock()
		defer recordsMu.Unlock()
		deadline, ok := ctx.Deadline()
		if recording && ok {
			records = append(records, recordedRuntimeContext{operation: operation, threadID: req.Ref.ThreadID, deadline: deadline})
		}
	}
	fixture.h.codexSessions.writeState = func(string, []byte) error {
		fixture.agent.mu.Lock()
		fixture.agent.handoffErrors["thread-a"] = errors.New("恢复 A 失败")
		fixture.agent.handoffErrors["thread-b"] = errors.New("恢复 B 失败")
		fixture.agent.inspectErrors["thread-a"] = errors.New("校准 A 失败")
		fixture.agent.inspectErrors["thread-b"] = errors.New("校准 B 失败")
		fixture.agent.mu.Unlock()
		recordsMu.Lock()
		recording = true
		recordsMu.Unlock()
		return errors.New("写盘失败")
	}
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if !errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("error=%v", err)
	}
	assertSharedCleanupDeadline(t, records)
}

func TestCompensationExpiredCleanupDeadlineIsNeverRenewed(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	sharedCleanup, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	sharedDeadline, _ := sharedCleanup.Deadline()
	records := make([]recordedRuntimeContext, 0, 4)
	fixture.agent.recordRuntimeContext = func(operation string, ctx context.Context, req agent.CodexRuntimeRequest) {
		deadline, ok := ctx.Deadline()
		if ok {
			records = append(records, recordedRuntimeContext{operation: operation, threadID: req.Ref.ThreadID, deadline: deadline})
		}
	}
	fixture.agent.rejectCanceledContext = true
	fixture.agent.handoffReleases["thread-a"] = make(chan struct{})
	change := codexRuntimeIntentChange{
		threadID: "thread-a", route: fixture.request("thread-a").route,
		before: codexControlIntent{Owner: codexControlDesktop, Revision: 1},
		after: codexControlIntent{
			Owner: codexControlRemote, RouteBindingKey: fixture.bindingKey,
			ConversationID: fixture.request("thread-a").route.conversationID, Revision: 2,
		},
	}
	if err := fixture.h.compensateCodexRuntimeChanges(sharedCleanup, fixture.agent, []codexRuntimeIntentChange{change}); err == nil {
		t.Fatal("已到期共享 cleanup 的补偿应失败并进入 conflict")
	}
	seen := make(map[string]bool)
	for _, record := range records {
		seen[record.operation] = true
		if record.deadline.After(sharedDeadline) {
			t.Fatalf("%s deadline被续期: got=%v shared=%v", record.operation, record.deadline, sharedDeadline)
		}
	}
	for _, operation := range []string{"handoff", "inspect", "mark"} {
		if !seen[operation] {
			t.Fatalf("records=%#v, 缺少%s deadline", records, operation)
		}
	}
}

func assertSharedCleanupDeadline(t *testing.T, records []recordedRuntimeContext) {
	t.Helper()
	var outerDeadline time.Time
	threads := make(map[string]bool)
	seen := make(map[string]map[string]bool)
	for _, record := range records {
		if seen[record.threadID] == nil {
			seen[record.threadID] = make(map[string]bool)
		}
		seen[record.threadID][record.operation] = true
		if record.operation == "handoff" {
			threads[record.threadID] = true
			if outerDeadline.IsZero() {
				outerDeadline = record.deadline
			} else if !record.deadline.Equal(outerDeadline) {
				t.Fatalf("handoff deadlines differ: %#v", records)
			}
		}
	}
	if len(threads) != 2 || outerDeadline.IsZero() {
		t.Fatalf("records=%#v, want two compensation threads", records)
	}
	for threadID := range threads {
		for _, operation := range []string{"handoff", "inspect", "mark"} {
			if !seen[threadID][operation] {
				t.Fatalf("records=%#v, %s缺少%s deadline", records, threadID, operation)
			}
		}
	}
	for _, record := range records {
		if record.deadline.After(outerDeadline) {
			t.Fatalf("%s/%s deadline延后: got=%v outer=%v", record.operation, record.threadID, record.deadline, outerDeadline)
		}
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
