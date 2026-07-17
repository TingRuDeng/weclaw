package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestAcquireCodexSessionDeadlineDuringOldReleaseKeepsTarget(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	request := fixture.request("thread-b")
	request.ctx = ctx
	fixture.agent.rejectCanceledContext = true
	fixture.agent.handoffReleases["thread-a"] = make(chan struct{})
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("target owner=%#v", got)
	}
	assertFakeCodexBindingOwner(t, fixture.agent, "thread-b", agent.CodexControlRemote)
}

func TestAcquireCodexSessionCanceledOldReleaseKeepsTarget(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	request := fixture.request("thread-b")
	request.ctx = ctx
	fixture.agent.rejectCanceledContext = true
	fixture.agent.handoffReleases["thread-a"] = make(chan struct{})
	fixture.agent.handoffHooks["thread-a"] = cancel
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("target owner=%#v", got)
	}
	assertFakeCodexBindingOwner(t, fixture.agent, "thread-b", agent.CodexControlRemote)
}

func TestAcquireCodexSessionOldReleaseFailureDoesNotProbeOrRollback(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffAfterErrors["thread-a"] = errors.New("checkpoint 读取失败")
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	if fixture.agent.bindCalls != 0 {
		t.Fatalf("inspect count=%d, 失败后不得校准探测", fixture.agent.bindCalls)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-a"); got.Owner != codexControlDesktop {
		t.Fatalf("old owner=%#v", got)
	}
	assertFakeCodexBindingOwner(t, fixture.agent, "thread-b", agent.CodexControlRemote)
}

func TestAcquireCodexSessionTargetFailureKeepsOwnerWithoutInventingConflict(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffAfterErrors["thread-b"] = errors.New("恢复后读取失败")
	fixture.agent.inspectErrors["thread-b"] = errors.New("校准失败")
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("error=%v result=%#v", err, result)
	}
	binding := fixture.agent.threadBinding("thread-b")
	if binding.Runtime == agent.CodexRuntimeConflict || binding.ConflictReason != "" {
		t.Fatalf("技术恢复失败不应伪造写入冲突，binding=%#v", binding)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("target owner=%#v", got)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-a"); got.Owner != codexControlDesktop {
		t.Fatalf("旧会话已释放的 owner 不得因目标 runtime 失败而恢复: %#v", got)
	}
	requests := fixture.agent.handoffRequests()
	if len(requests) != 2 || requests[1].Ref.ThreadID != "thread-a" || requests[1].Intent.Owner != agent.CodexControlDesktop {
		t.Fatalf("目标失败后仍必须关闭旧远程 writer，handoff=%#v", requests)
	}
	assertFakeCodexBindingOwner(t, fixture.agent, "thread-a", agent.CodexControlDesktop)
}

func TestFailCodexAcquireRuntimeDoesNotOverwriteNewerOwner(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	request := fixture.request("thread-b")
	staleIntent := agent.CodexControlIntent{
		Owner: agent.CodexControlRemote, RouteKey: fixture.bindingKey,
		ConversationID: request.route.conversationID, Revision: 2,
	}
	newerIntent := agent.CodexControlIntent{
		Owner: agent.CodexControlRemote, RouteKey: "new-route",
		ConversationID: "new-conversation", Revision: 3,
	}
	fixture.agent.setThreadBinding("thread-b", agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeDesktop, Control: newerIntent,
		State: agent.CodexThreadState{ThreadID: "thread-b"},
	})
	result := codexSessionAcquireResult{resolution: codexRuntimeResolution{
		Request: agent.CodexRuntimeRequest{Ref: request.route.ref("thread-b"), Intent: staleIntent},
	}}

	result = fixture.h.failCodexAcquireRuntime(result, fixture.agent, errors.New("观察状态已变化"))

	if result.runtimeErr == nil {
		t.Fatalf("runtimeErr=%v，观察失败应保留为可重试技术错误", result.runtimeErr)
	}
	binding := fixture.agent.threadBinding("thread-b")
	if binding.Control != newerIntent || binding.Runtime != agent.CodexRuntimeDesktop {
		t.Fatalf("binding=%#v，旧失败路径覆盖了新 owner", binding)
	}
}

func TestAcquireCodexSessionStoreFailureDoesNotTouchRuntime(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.h.codexSessions.SetFilePath(t.TempDir() + "/codex-sessions.json")
	fixture.h.codexSessions.writeState = func(string, []byte) error { return errors.New("写盘失败") }
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err == nil {
		t.Fatalf("error=%v", err)
	}
	if len(fixture.agent.handoffRequests()) != 0 {
		t.Fatalf("handoff=%#v", fixture.agent.handoffRequests())
	}
}

func TestAcquireCodexSessionExplicitHandoffRecoversMarkedConflict(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	request := fixture.request("thread-b")
	request.forceRuntimeHandoff = true
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
	var recordsMu sync.Mutex
	records := make([]recordedRuntimeContext, 0, 8)
	fixture.agent.recordRuntimeContext = func(operation string, ctx context.Context, req agent.CodexRuntimeRequest) {
		recordsMu.Lock()
		defer recordsMu.Unlock()
		deadline, ok := ctx.Deadline()
		if ok {
			records = append(records, recordedRuntimeContext{operation: operation, threadID: req.Ref.ThreadID, deadline: deadline})
		}
	}
	fixture.agent.handoffErrors["thread-a"] = errors.New("恢复 A 失败")
	fixture.agent.handoffErrors["thread-b"] = errors.New("恢复 B 失败")
	changes := []codexRuntimeIntentChange{
		{
			threadID: "thread-a", route: fixture.request("thread-a").route,
			before: codexControlIntent{Owner: codexControlDesktop, Revision: 1},
			after:  fixture.h.codexSessions.controlIntent("thread-a"),
		},
		{
			threadID: "thread-b", route: fixture.request("thread-b").route,
			before: codexControlIntent{Owner: codexControlRemote, RouteBindingKey: fixture.bindingKey, ConversationID: fixture.request("thread-b").route.conversationID, Revision: 2},
			after:  codexControlIntent{Owner: codexControlDesktop, Revision: 1},
		},
	}
	cleanupCtx, cancel := newCodexSessionAcquireCleanupContext(context.Background())
	defer cancel()
	if err := fixture.h.compensateCodexRuntimeChanges(cleanupCtx, fixture.agent, changes); err == nil {
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
		t.Fatal("已到期共享 cleanup 的补偿应失败")
	}
	seen := make(map[string]bool)
	for _, record := range records {
		seen[record.operation] = true
		if record.deadline.After(sharedDeadline) {
			t.Fatalf("%s deadline被续期: got=%v shared=%v", record.operation, record.deadline, sharedDeadline)
		}
	}
	for _, operation := range []string{"handoff"} {
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
		for _, operation := range []string{"handoff"} {
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

func TestCodexSessionAcquireCleanupContextIgnoresFutureParentCancel(t *testing.T) {
	type cleanupContextKey struct{}
	parent, cancelParent := context.WithCancel(context.WithValue(
		context.Background(), cleanupContextKey{}, "trace-value",
	))
	cleanup, cancelCleanup := newCodexSessionAcquireCleanupContext(parent)
	defer cancelCleanup()
	deadlineBefore, ok := cleanup.Deadline()
	if !ok {
		t.Fatal("cleanup 必须有有限 deadline")
	}
	cancelParent()
	deadlineAfter, stillHasDeadline := cleanup.Deadline()
	if cleanup.Err() != nil || cleanup.Value(cleanupContextKey{}) != "trace-value" {
		t.Fatalf("cleanup err=%v value=%v", cleanup.Err(), cleanup.Value(cleanupContextKey{}))
	}
	if !stillHasDeadline || !deadlineAfter.Equal(deadlineBefore) || time.Until(deadlineAfter) <= 0 ||
		time.Until(deadlineAfter) > codexSessionAcquireCleanupTimeout {
		t.Fatalf("deadline before=%v after=%v ok=%v", deadlineBefore, deadlineAfter, stillHasDeadline)
	}
}

func assertFakeCodexBindingOwner(t *testing.T, ag *fakeCodexLiveAgent, threadID string, owner agent.CodexControlOwner) {
	t.Helper()
	if binding := ag.threadBinding(threadID); binding.Control.Owner != owner {
		t.Fatalf("thread=%s binding=%#v, want owner=%s", threadID, binding, owner)
	}
}
