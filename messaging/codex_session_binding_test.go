package messaging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type codexSessionBindingFixture struct {
	h          *Handler
	ag         *fakeCodexLiveAgent
	routeUser  string
	bindingKey string
	workspaceA string
	workspaceB string
	reply      *platformtest.Replier
}

type codexRuntimeRecoveryReferenceReplier struct {
	*codexFollowerRouteReplier
	reference platform.DurableCommandResultReference
}

type readyGateCodexLiveAgent struct {
	*fakeCodexLiveAgent
	readyEntered chan struct{}
	readyRelease <-chan struct{}
	readyErr     error
}

type failingCodexSubscriptionAgent struct {
	*fakeCodexLiveAgent
	err   error
	calls int
}

func (a *failingCodexSubscriptionAgent) SubscribeCodexThread(
	context.Context,
	string,
	string,
) (bool, error) {
	a.calls++
	return true, a.err
}

func (a *readyGateCodexLiveAgent) WatchCodexThreadEventsForTurnReady(
	ctx context.Context,
	conversationID string,
	threadID string,
	turnID string,
	onReady func(agent.CodexThreadObserverReady) error,
	onProgress func(agent.ProgressEvent),
) (string, error) {
	signalCodexLiveTestHook(a.readyEntered)
	if err := waitCodexLiveTestHook(ctx, a.readyRelease); err != nil {
		return "", err
	}
	if a.readyErr != nil {
		return "", a.readyErr
	}
	binding := a.threadBinding(threadID)
	if onReady != nil {
		if err := onReady(agent.CodexThreadObserverReady{
			ThreadID: threadID, TurnID: turnID, RuntimeGeneration: binding.RuntimeGeneration,
		}); err != nil {
			return "", err
		}
	}
	return a.WatchCodexThread(ctx, conversationID, threadID, textProgressCallback(onProgress))
}

func (r *codexRuntimeRecoveryReferenceReplier) DurableCommandResultReference() (platform.DurableCommandResultReference, error) {
	return r.reference, nil
}

func newCodexSessionBindingFixture(t *testing.T) *codexSessionBindingFixture {
	t.Helper()
	h := NewHandler(nil, nil)
	f := &codexSessionBindingFixture{
		h: h, routeUser: "route-user",
		workspaceA: "/workspace/a", workspaceB: "/workspace/b",
		reply: platformtest.NewReplier(platform.Capabilities{Text: true}),
	}
	f.bindingKey = codexBindingKey(f.routeUser, "codex")
	h.SetPlatformRegistry(platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &codexFollowerTestPlatform{
			name: platform.PlatformFeishu, account: "cli_a", reply: f.reply,
		},
		Access: platform.NewAccessControl([]string{f.routeUser}),
	}}))
	h.ensureCodexSessions().setThread(f.bindingKey, f.workspaceA, "thread-a")
	h.ensureCodexSessions().setThread(f.bindingKey, f.workspaceB, "thread-b")
	h.ensureCodexSessions().setActiveWorkspace(f.bindingKey, f.workspaceA)
	f.ag = newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	for _, threadID := range []string{"thread-a", "thread-b"} {
		f.ag.setThreadBinding(threadID, agent.CodexThreadBinding{
			Runtime: agent.CodexRuntimeWeClaw,
			State:   agent.CodexThreadState{ThreadID: threadID},
		})
	}
	return f
}

func (f *codexSessionBindingFixture) request(threadID string) codexSessionAcquireRequest {
	workspace := f.workspaceB
	if threadID == "thread-a" {
		workspace = f.workspaceA
	}
	return codexSessionAcquireRequest{
		ctx: context.Background(), actorUserID: f.routeUser, authorizedIdentity: f.routeUser,
		routeUserID: f.routeUser,
		agentName:   "codex", agent: f.ag,
		route: codexConversationRoute{
			bindingKey: f.bindingKey, workspaceRoot: workspace,
			conversationID: buildCodexConversationID(f.routeUser, "codex", workspace),
			threadID:       threadID,
		},
		platform: platform.PlatformWeChat, reply: f.reply,
	}
}

func (f *codexSessionBindingFixture) setActiveTarget(turnID string) {
	state := agent.CodexThreadState{ThreadID: "thread-b", Active: true, ActiveTurnID: turnID}
	f.ag.setBindingState(state)
	f.ag.setThreadBinding("thread-b", agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw, State: state})
}

func TestAcquireCodexSessionCommitsFrontendBindingAndSharedRuntime(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceB {
		t.Fatalf("active workspace=%q", active)
	}
	if threadID, pending := f.h.ensureCodexSessions().getThread(f.bindingKey, f.workspaceB); pending || threadID != "thread-b" {
		t.Fatalf("thread=%q pending=%v", threadID, pending)
	}
	requests := f.ag.handoffRequests()
	if len(requests) != 1 || requests[0].Ref.ThreadID != "thread-b" ||
		requests[0].Intent.RouteKey != f.bindingKey {
		t.Fatalf("bind requests=%#v", requests)
	}
	if result.resolution.Binding.Runtime != agent.CodexRuntimeWeClaw {
		t.Fatalf("runtime=%q", result.resolution.Binding.Runtime)
	}
}

func TestAcquireCodexSessionCannotRestoreFollowerAfterConcurrentRevocation(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	f.ag.handoffEntered = entered
	f.ag.handoffRelease = release
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}
	req := f.request("thread-b")
	req.platform = platform.PlatformFeishu
	req.accountID = "cli_a"
	req.reply = &codexFollowerRouteReplier{Replier: f.reply, route: route}

	type acquireResult struct {
		result codexSessionAcquireResult
		err    error
	}
	acquired := make(chan acquireResult, 1)
	go func() {
		result, err := f.h.acquireCodexSessionWithBindingLocked(req)
		acquired <- acquireResult{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("runtime handoff did not start")
	}
	revoked := make(chan struct{})
	go func() {
		f.h.refreshFeishuAccountAccess("cli_a", nil)
		close(revoked)
	}()
	select {
	case <-revoked:
		t.Fatal("revocation returned before follower acquire reached its commit boundary")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	got := <-acquired
	if got.err != nil || got.result.runtimeErr != nil {
		t.Fatalf("acquire result=%#v err=%v", got.result, got.err)
	}
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("revocation did not finish after acquire released its delivery lease")
	}
	if snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey); ok {
		t.Fatalf("revoked follower was restored after command return: %#v", snapshot)
	}
}

func TestAcquireCodexSessionUnsubscribesUnusedOldThreadAfterBindingTarget(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.threadHandoffApplicable = true

	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || result.runtimeErr != nil || result.handoffReleaseErr != nil || !result.handoffReleaseAttempted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	threads, operations := f.ag.threadHandoffSnapshot()
	if len(threads) != 1 || threads[0] != "thread-a" {
		t.Fatalf("released threads=%v, want thread-a", threads)
	}
	if len(operations) < 2 || operations[0] != "bind:thread-b" || operations[1] != "unsubscribe:thread-a" {
		t.Fatalf("operations=%v, want confirmed target bind before old subscription cleanup", operations)
	}
	if text := f.h.renderCodexSessionAcquireSuccess(result); !strings.Contains(text, "旧会话: 已停止当前连接观察") {
		t.Fatalf("text=%q, want unsubscribe notice", text)
	}
}

func TestAcquireCodexSessionKeepsOldThreadWhenAnotherFrontendUsesIt(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.threadHandoffApplicable = true
	otherBinding := codexBindingKey("other-route", "codex")
	otherWorkspace := "/workspace/other"
	f.h.ensureCodexSessions().setThread(otherBinding, otherWorkspace, "thread-a")
	f.h.ensureCodexSessions().setActiveWorkspace(otherBinding, otherWorkspace)

	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	threads, _ := f.ag.threadHandoffSnapshot()
	if len(threads) != 0 || result.handoffReleaseAttempted || !result.handoffReleaseRetained {
		t.Fatalf("release calls=%v result=%#v", threads, result)
	}
	if text := f.h.renderCodexSessionAcquireSuccess(result); !strings.Contains(text, "旧会话: 仍被其他窗口选中") {
		t.Fatalf("text=%q, want retained frontend notice", text)
	}
}

func TestAcquireCodexSessionUnsubscribesPreviousThreadForPendingFirstTurn(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.threadHandoffApplicable = true
	request := f.request("thread-b")
	request.pendingFirstTurn = true

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	threads, _ := f.ag.threadHandoffSnapshot()
	if len(threads) != 1 || threads[0] != "thread-a" || !result.handoffReleaseAttempted {
		t.Fatalf("unsubscribe calls=%v result=%#v", threads, result)
	}
	f.ag.mu.Lock()
	providerPrepareRequest := f.ag.providerPrepareRequest
	f.ag.mu.Unlock()
	if !providerPrepareRequest.PendingFirstTurn {
		t.Fatal("pending-first-turn must reach provider preparation and runtime binding before store commit")
	}
}

func TestAcquireCodexSessionKeepsBindingWhenOldThreadReleaseIsBusy(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.threadHandoffApplicable = true
	f.ag.threadHandoffErr = agent.ErrCodexWriterBusy

	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || result.runtimeErr != nil || !result.handoffReleaseAttempted ||
		!errors.Is(result.handoffReleaseErr, agent.ErrCodexWriterBusy) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceB {
		t.Fatalf("active workspace=%q, want %q", active, f.workspaceB)
	}
	if got := len(f.ag.handoffRequests()); got != 1 {
		t.Fatalf("target bind calls=%d, want 1", got)
	}
	if text := f.h.renderCodexSessionAcquireSuccess(result); !strings.Contains(text, "当前连接停止观察失败") ||
		!strings.Contains(text, "不影响新会话写入") {
		t.Fatalf("text=%q, want non-blocking unsubscribe failure notice", text)
	}
}

func TestAcquireCodexSessionPreservesCommandDeadlineForRuntimeHandoff(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.h.codexControlTimeout = 20 * time.Millisecond
	commandCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	commandDeadline, _ := commandCtx.Deadline()
	request := f.request("thread-b")
	request.ctx = commandCtx
	var handoffDeadline time.Time
	f.ag.recordRuntimeContext = func(phase string, ctx context.Context, _ agent.CodexRuntimeRequest) {
		if phase == "handoff" {
			handoffDeadline, _ = ctx.Deadline()
		}
	}

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)

	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if handoffDeadline.IsZero() || !handoffDeadline.Equal(commandDeadline) {
		t.Fatalf("handoff deadline=%v, want command deadline=%v", handoffDeadline, commandDeadline)
	}
}

func TestAcquireCodexSessionPreparesProviderBeforeSharedRuntimeBinding(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("acquire result=%+v err=%v", result, err)
	}
	f.ag.mu.Lock()
	defer f.ag.mu.Unlock()
	if f.ag.providerPrepareCalls != 1 || !f.ag.providerPrepared || f.ag.handoffCalls != 1 || f.ag.handoffBeforeProviderPrepare {
		t.Fatalf("prepare=%d handoff=%d", f.ag.providerPrepareCalls, f.ag.handoffCalls)
	}
	if f.ag.lastRuntimeReq.WorkspaceRoot != f.workspaceB {
		t.Fatalf("workspaceRoot=%q, want %q", f.ag.lastRuntimeReq.WorkspaceRoot, f.workspaceB)
	}
}

func TestAcquireCodexSessionDefersProviderMigrationWithoutResumingWrongProvider(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.providerPreparation = agent.CodexProviderPreparation{Provider: "openai", PreviousProvider: "relay", Deferred: true}
	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || !errors.Is(result.runtimeErr, agent.ErrCodexWriterBusy) {
		t.Fatalf("acquire result=%+v err=%v", result, err)
	}
	f.ag.mu.Lock()
	handoffCalls := f.ag.handoffCalls
	f.ag.mu.Unlock()
	if handoffCalls != 0 {
		t.Fatalf("handoff calls=%d, want 0", handoffCalls)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceB {
		t.Fatalf("durable workspace=%q, want %q", active, f.workspaceB)
	}
}

func TestAcquireCodexSessionKeepsObservingActiveTargetUntilNextTurnCanMigrate(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.setActiveTarget("turn-live")
	f.ag.providerPreparation = agent.CodexProviderPreparation{
		Provider: "openai", PreviousProvider: "relay", Deferred: true, TargetActive: true,
	}
	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("acquire result=%+v err=%v", result, err)
	}
	f.ag.mu.Lock()
	handoffCalls := f.ag.handoffCalls
	f.ag.mu.Unlock()
	if handoffCalls != 1 {
		t.Fatalf("handoff calls=%d, want 1", handoffCalls)
	}
}

func TestAcquireCodexSessionUnreadableTargetKeepsPreviousFrontendBinding(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.threadStateErr = context.DeadlineExceeded
	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want target read failure", err)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceA {
		t.Fatalf("active workspace=%q, want previous %q", active, f.workspaceA)
	}
	if got := len(f.ag.handoffRequests()); got != 0 {
		t.Fatalf("shared host binding started before target validation: %d calls", got)
	}
}

func TestAcquireCodexSessionBindFailureRollsBackFrontendBinding(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.handoffErrors["thread-b"] = agent.ErrCodexRuntimeUnavailable
	if err := f.h.ensureAgentSessions().Set(f.routeUser, "claude"); err != nil {
		t.Fatal(err)
	}

	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))

	if !errors.Is(err, agent.ErrCodexRuntimeUnavailable) {
		t.Fatalf("err=%v, want runtime bind failure", err)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceA {
		t.Fatalf("active workspace=%q, want previous %q", active, f.workspaceA)
	}
	if threadID, pending := f.h.ensureCodexSessions().getThread(f.bindingKey, f.workspaceA); pending || threadID != "thread-a" {
		t.Fatalf("previous thread=%q pending=%v", threadID, pending)
	}
	if _, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey); ok {
		t.Fatal("failed bind must not retain a follower for the uncommitted target")
	}
	if selected, ok := f.h.ensureAgentSessions().Get(f.routeUser); !ok || selected != "claude" {
		t.Fatalf("selected agent=%q ok=%v, want previous claude selection", selected, ok)
	}
}

func TestAcquireCodexSessionBindFailureRemovesNewAgentSelectionWhenPreviouslyUnset(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.handoffErrors["thread-b"] = agent.ErrCodexRuntimeUnavailable

	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))

	if !errors.Is(err, agent.ErrCodexRuntimeUnavailable) {
		t.Fatalf("err=%v, want runtime bind failure", err)
	}
	if selected, ok := f.h.ensureAgentSessions().Get(f.routeUser); ok {
		t.Fatalf("selected agent=%q, failed acquire must restore the absent selection", selected)
	}
}

func TestAcquireCodexSessionBindFailureRestoresGlobalCwdWithoutPreviousBinding(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	store := f.h.ensureCodexSessions()
	store.mu.Lock()
	delete(store.bindings, f.bindingKey)
	store.mu.Unlock()
	previousCwd := "/workspace/runtime-default"
	f.h.switchCodexWorkspace("codex", previousCwd, f.ag)
	f.ag.handoffErrors["thread-b"] = agent.ErrCodexRuntimeUnavailable

	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))

	if !errors.Is(err, agent.ErrCodexRuntimeUnavailable) {
		t.Fatalf("err=%v, want runtime bind failure", err)
	}
	if got := f.ag.lastWorkingDir(); got != previousCwd {
		t.Fatalf("runtime cwd=%q, want previous %q", got, previousCwd)
	}
	if got := f.h.codexWorkspaceRoot("codex"); got != previousCwd {
		t.Fatalf("handler cwd=%q, want previous %q", got, previousCwd)
	}
}

func TestAcquireCodexSessionPersistenceFailureSkipsRuntime(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.h.ensureCodexSessions().SetFilePath(filepath.Join(t.TempDir(), "codex-sessions.json"))
	f.h.ensureCodexSessions().writeState = func(string, []byte) error { return errors.New("disk full") }
	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err == nil {
		t.Fatal("persistence failure was ignored")
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceA {
		t.Fatalf("live binding changed to %q", active)
	}
	if len(f.ag.handoffRequests()) != 0 {
		t.Fatal("runtime touched before durable binding commit")
	}
}

func TestAcquireCodexSessionAgentSelectionFailureRollsBackBinding(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	statePath := filepath.Join(t.TempDir(), "agent-sessions.json")
	if err := f.h.SetAgentSessionFile(statePath); err != nil {
		t.Fatal(err)
	}
	if err := f.h.ensureAgentSessions().Set(f.routeUser, "claude"); err != nil {
		t.Fatal(err)
	}
	invalidParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.h.ensureAgentSessions().filePath = filepath.Join(invalidParent, "state.json")
	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err == nil {
		t.Fatal("agent selection failure was ignored")
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceA {
		t.Fatalf("binding was not rolled back: %q", active)
	}
	if len(f.ag.handoffRequests()) != 0 {
		t.Fatal("runtime touched after agent selection failure")
	}
}

func TestAcquireCodexSessionSameConversationActiveTurnKeepsOldThread(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	request := f.request("thread-b")
	f.ag.threadHandoffApplicable = true
	task, _, started := f.h.beginActiveTask(context.Background(), request.route.conversationID, activeTaskMeta{
		owner: f.routeUser, routeUserID: f.routeUser, agentName: "codex",
		codexThreadID: "thread-a", codexTurnID: "turn-a",
	})
	if !started {
		t.Fatal("failed to create active task")
	}
	defer f.h.finishActiveTask(request.route.conversationID, task)
	result, err := f.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if retained := result.handoffReleaseRetainedByTask; !retained {
		t.Fatalf("result=%#v, want old thread retained for active task", result)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceB {
		t.Fatalf("active workspace=%q, want %q", active, f.workspaceB)
	}
	if threads, _ := f.ag.threadHandoffSnapshot(); len(threads) != 0 {
		t.Fatalf("old active thread was handed off: %v", threads)
	}
}

func TestAcquireCodexSessionDifferentFrontendDoesNotAbandonRunningTask(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.threadHandoffApplicable = true
	oldConversation := buildCodexConversationID(f.routeUser, "codex", f.workspaceA)
	task, _, started := f.h.beginActiveTask(context.Background(), oldConversation, activeTaskMeta{
		owner: f.routeUser, routeUserID: f.routeUser, agentName: "codex",
		codexThreadID: "thread-a", codexTurnID: "turn-a",
	})
	if !started {
		t.Fatal("failed to create old task")
	}
	defer f.h.finishActiveTask(oldConversation, task)
	if _, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b")); err != nil {
		t.Fatal(err)
	}
	if current, active := f.h.activeTask(oldConversation); !active || current != task {
		t.Fatal("binding another conversation abandoned a running task")
	}
	threads, _ := f.ag.threadHandoffSnapshot()
	if len(threads) != 0 {
		t.Fatalf("old active thread was handed off: %v", threads)
	}
}

func TestAcquireCodexSessionActiveSharedTurnStartsObserver(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.setActiveTarget("turn-b")
	f.ag.watchDone = make(chan struct{})
	t.Cleanup(func() { closeTestChannel(f.ag.watchDone) })
	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || result.runtimeErr != nil || !result.externalActive {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.externalProgressCard {
		t.Fatal("text-only WeChat observer should keep inline task details")
	}
	task, active := f.h.activeTask(result.route.conversationID)
	if !active || task.codexThreadID != "thread-b" || task.codexTurnID != "turn-b" {
		t.Fatalf("active=%v task=%#v", active, task)
	}
	closeTestChannel(f.ag.watchDone)
	waitDone(t, task.done, "shared host observer cleanup")
}

func TestAcquireCodexSessionFeishuActiveTurnUsesDedicatedProgressCard(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}
	f.reply = platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	routeReply := &codexFollowerRouteReplier{Replier: f.reply, route: route}
	f.setActiveTarget("turn-b")
	f.ag.watchDone = make(chan struct{})
	t.Cleanup(func() { closeTestChannel(f.ag.watchDone) })
	request := f.request("thread-b")
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = routeReply

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || result.runtimeErr != nil || !result.externalActive || !result.externalProgressCard {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	text := f.h.renderCodexSessionAcquireSuccess(result)
	if !strings.Contains(text, "进度和结果见下方任务卡") || strings.Contains(text, "共享 Codex 任务正在进行") {
		t.Fatalf("text=%q, want compact dedicated task card notice", text)
	}
	snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok || snapshot.FollowTurnID != "turn-b" || !snapshot.FollowTurnPending {
		t.Fatalf("active acquire follower snapshot=%#v ok=%v, want pending turn-b", snapshot, ok)
	}

	closeTestChannel(f.ag.watchDone)
	task, active := f.h.activeTask(result.route.conversationID)
	if active {
		waitDone(t, task.done, "feishu shared host observer cleanup")
	}
}

func TestAcquireCodexSessionWaitsForCardAndExactTurnObserverReadiness(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.setActiveTarget("turn-b")
	binding := f.ag.threadBinding("thread-b")
	binding.RuntimeGeneration = 7
	f.ag.setThreadBinding("thread-b", binding)
	f.ag.watchDone = make(chan struct{})
	t.Cleanup(func() { closeTestChannel(f.ag.watchDone) })
	readyEntered := make(chan struct{}, 1)
	readyRelease := make(chan struct{})
	ag := &readyGateCodexLiveAgent{
		fakeCodexLiveAgent: f.ag, readyEntered: readyEntered, readyRelease: readyRelease,
	}
	f.reply = platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	request := f.request("thread-b")
	request.agent = ag
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = &codexFollowerRouteReplier{Replier: f.reply, route: platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}}
	type acquireResult struct {
		result codexSessionAcquireResult
		err    error
	}
	done := make(chan acquireResult, 1)
	go func() {
		result, err := f.h.acquireCodexSessionWithBindingLocked(request)
		done <- acquireResult{result: result, err: err}
	}()
	select {
	case <-readyEntered:
	case <-time.After(time.Second):
		t.Fatal("observer did not reach readiness gate")
	}
	select {
	case got := <-done:
		t.Fatalf("acquire returned before observer readiness: %#v", got)
	case <-time.After(30 * time.Millisecond):
	}
	preparing, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok || preparing.AttachPhase != codexFollowerAttachPreparing ||
		preparing.AttachTurnID != "turn-b" || preparing.RuntimeGeneration != 7 {
		t.Fatalf("preparing follower=%#v ok=%v", preparing, ok)
	}
	close(readyRelease)
	got := <-done
	if got.err != nil || got.result.runtimeErr != nil || !got.result.externalActive {
		t.Fatalf("result=%#v err=%v", got.result, got.err)
	}
	ready := f.h.ensureCodexSessions().followerSnapshots()[0]
	if ready.AttachPhase != codexFollowerAttachReady || ready.AttachTurnID != "turn-b" || ready.RuntimeGeneration != 7 {
		t.Fatalf("ready follower=%#v", ready)
	}
	closeTestChannel(f.ag.watchDone)
}

func TestAcquireSameActiveCodexSessionCommitsNewAttachRevisionReady(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.setActiveTarget("turn-b")
	binding := f.ag.threadBinding("thread-b")
	binding.RuntimeGeneration = 7
	f.ag.setThreadBinding("thread-b", binding)
	f.ag.watchDone = make(chan struct{})
	t.Cleanup(func() { closeTestChannel(f.ag.watchDone) })
	f.reply = platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	request := f.request("thread-b")
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = &codexFollowerRouteReplier{Replier: f.reply, route: platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}}

	firstResult, err := f.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || firstResult.runtimeErr != nil || !firstResult.externalActive {
		t.Fatalf("first result=%#v err=%v", firstResult, err)
	}
	first, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok || first.AttachPhase != codexFollowerAttachReady {
		t.Fatalf("first attach=%#v ok=%v", first, ok)
	}

	secondResult, err := f.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || secondResult.runtimeErr != nil || !secondResult.externalActive {
		t.Fatalf("second result=%#v err=%v", secondResult, err)
	}
	second, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok || second.AttachRevision <= first.AttachRevision ||
		second.AttachPhase != codexFollowerAttachReady || second.AttachTurnID != "turn-b" ||
		second.RuntimeGeneration != 7 {
		t.Fatalf("second attach=%#v ok=%v, first revision=%d", second, ok, first.AttachRevision)
	}
}

func TestAcquireCodexSessionReusesReadyInProcessCardWithoutObserverControl(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.h.ensureCodexSessions().setActiveWorkspace(f.bindingKey, f.workspaceB)
	f.setActiveTarget("turn-b")
	binding := f.ag.threadBinding("thread-b")
	binding.RuntimeGeneration = 7
	f.ag.setThreadBinding("thread-b", binding)
	f.reply = platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	routeReply := &codexFollowerRouteReplier{Replier: f.reply, route: platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}}
	request := f.request("thread-b")
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = routeReply
	task, taskCtx, started := f.h.beginActiveTask(context.Background(), request.route.conversationID, activeTaskMeta{
		owner: f.routeUser, routeUserID: f.routeUser, agentName: "codex", message: "本地活动任务",
		codexThreadID: "thread-b", codexTurnID: "turn-b", inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("in-process task should start")
	}
	request.taskContext = taskCtx
	progressCfg := config.DefaultProgressConfig()
	progressCfg.Mode = progressModeStream
	progressCfg.SendAcceptance = boolPtr(false)
	_, _, progress := f.h.startProgressSessionForWorkspaceAgentWithHandle(
		taskCtx, routeReply, "", "codex", f.workspaceB, "本地活动任务", progressCfg,
	)
	if progress == nil || progress.nativeProgressReadyError() != nil {
		t.Fatalf("native progress card not ready: %#v", progress)
	}
	task.attachProgressSession(progress)
	t.Cleanup(func() {
		task.detachProgressSession(progress)
		progress.stopBackground()
		task.cancel()
		f.h.finishActiveTask(request.route.conversationID, task)
	})

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)

	if err != nil || result.runtimeErr != nil || !result.externalActive || !result.externalProgressCard {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	current, active := f.h.activeTask(request.route.conversationID)
	if !active || current != task || current.externalReservation != nil {
		t.Fatalf("active=%t task=%p current=%p reservation=%#v", active, task, current, current.externalReservation)
	}
	snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok || snapshot.AttachPhase != codexFollowerAttachReady ||
		snapshot.AttachTurnID != "turn-b" || snapshot.RuntimeGeneration != 7 {
		t.Fatalf("follower=%#v ok=%t", snapshot, ok)
	}
}

func TestAcquireCodexSessionCardFailureKeepsBindingWritableAndMarksSyncDegraded(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.setActiveTarget("turn-b")
	f.ag.watchDone = make(chan struct{})
	t.Cleanup(func() { closeTestChannel(f.ag.watchDone) })
	f.reply = platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	f.reply.OpenStreamErr = errors.New("card unavailable")
	request := f.request("thread-b")
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = &codexFollowerRouteReplier{Replier: f.reply, route: platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}}

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)

	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	text := f.h.renderCodexSessionAcquireSuccess(result)
	if !strings.Contains(text, "进度同步: 已降级") || strings.Contains(text, "运行通道: 暂不可用") {
		t.Fatalf("text=%q, want writable binding with degraded synchronization", text)
	}
	snapshot := f.h.ensureCodexSessions().followerSnapshots()[0]
	if snapshot.AttachPhase != codexFollowerAttachPreparing {
		t.Fatalf("card failure follower=%#v, want preparing", snapshot)
	}
}

func TestAcquireCodexSessionSubscriptionFailureKeepsBindingWritable(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.setActiveTarget("turn-b")
	service := &codexFollowerService{
		failures: make(map[string]codexFollowerFailureState), resultFailures: make(map[string]codexFollowerFailureState),
	}
	f.h.codexFollowerMu.Lock()
	f.h.codexFollower = service
	f.h.codexFollowerMu.Unlock()
	t.Cleanup(func() {
		f.h.codexFollowerMu.Lock()
		if f.h.codexFollower == service {
			f.h.codexFollower = nil
		}
		f.h.codexFollowerMu.Unlock()
	})
	wantErr := errors.New("observer subscription unavailable")
	ag := &failingCodexSubscriptionAgent{fakeCodexLiveAgent: f.ag, err: wantErr}
	request := f.request("thread-b")
	request.agent = ag
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = &codexFollowerRouteReplier{Replier: f.reply, route: platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}}

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)

	if err != nil || result.runtimeErr != nil || !errors.Is(result.syncErr, wantErr) || ag.calls != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", result, ag.calls, err)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceB {
		t.Fatalf("active workspace=%q, want committed target %q", active, f.workspaceB)
	}
	text := f.h.renderCodexSessionAcquireSuccess(result)
	if !strings.Contains(text, "进度同步: 已降级") || strings.Contains(text, "运行通道: 暂不可用") {
		t.Fatalf("text=%q, want writable binding with degraded subscription", text)
	}
	if line := f.h.codexProgressSyncStatusLine(f.bindingKey, "thread-b"); line != "进度同步: 已降级" {
		t.Fatalf("status line=%q, want degraded synchronization", line)
	}
}

func TestAcquireCodexSessionIdleTargetDoesNotSubscribeObserver(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	ag := &failingCodexSubscriptionAgent{
		fakeCodexLiveAgent: f.ag,
		err:                errors.New("idle target must not subscribe"),
	}
	request := f.request("thread-b")
	request.agent = ag
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = &codexFollowerRouteReplier{Replier: f.reply, route: platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}}

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)

	if err != nil || result.runtimeErr != nil || result.syncErr != nil || ag.calls != 0 {
		t.Fatalf("result=%#v calls=%d err=%v", result, ag.calls, err)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceB {
		t.Fatalf("active workspace=%q, want committed idle target %q", active, f.workspaceB)
	}
}

func TestAcquireCodexSessionIdleFollowerBaselinesHistoricalTurnAtBindingCommit(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.workspaceB = t.TempDir()
	f.h.ensureCodexSessions().setThread(f.bindingKey, f.workspaceB, "thread-b")
	codexDir := t.TempDir()
	writeLocalCodexSession(t, codexDir, "thread-b", f.workspaceB, "历史会话", "2026-08-12T08:00:00Z")
	rolloutPath := localRolloutPathForTest(codexDir, "thread-b")
	appendCodexRolloutRecord(t, rolloutPath, rolloutTaskStartedRecord("turn-before-binding"))
	appendCodexRolloutRecord(t, rolloutPath, rolloutTaskCompleteRecord("turn-before-binding", "历史回答"))
	f.h.SetCodexLocalSessionDir(codexDir)

	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}
	request := f.request("thread-b")
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = &codexFollowerRouteReplier{Replier: f.reply, route: route}

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok || !snapshot.FollowTurnInitialized || snapshot.FollowTurnPending ||
		snapshot.FollowTurnID != "turn-before-binding" {
		t.Fatalf("idle acquire follower snapshot=%#v ok=%v, want settled historical baseline", snapshot, ok)
	}
}

func TestAcquireCodexSessionIdleFollowerBaselinesRuntimeHistoryWithoutLocalRollout(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	state := agent.CodexThreadState{
		ThreadID: "thread-b", LastTurnID: "turn-desktop-history", LastTurnStatus: "completed",
		LastAgentMessageText: "Desktop 历史回答",
	}
	f.ag.setThreadBinding("thread-b", agent.CodexThreadBinding{Runtime: agent.CodexRuntimeDesktop, State: state})
	f.ag.setBindingState(state)
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}
	request := f.request("thread-b")
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = &codexFollowerRouteReplier{Replier: f.reply, route: route}

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok || !snapshot.FollowTurnInitialized || snapshot.FollowTurnPending ||
		snapshot.FollowTurnID != "turn-desktop-history" {
		t.Fatalf("runtime baseline snapshot=%#v ok=%v", snapshot, ok)
	}
}

func TestCodexFollowerBaselineInitializesEmptyThread(t *testing.T) {
	baseline := codexFollowerBaselineFromSources(
		codexRolloutTaskState{}, agent.CodexThreadBinding{
			Runtime: agent.CodexRuntimeDesktop, State: agent.CodexThreadState{ThreadID: "thread-empty"},
		}, nil,
	)
	if !baseline.initialized || baseline.pending || baseline.turnID != "" {
		t.Fatalf("empty thread baseline=%#v, want initialized empty settled cursor", baseline)
	}
}

func TestCodexFollowerBaselinePrefersAuthoritativeTerminalRuntimeOverStaleRollout(t *testing.T) {
	baseline := codexFollowerBaselineFromSources(
		codexRolloutTaskState{TurnID: "turn-before-binding", Active: true},
		agent.CodexThreadBinding{
			Runtime: agent.CodexRuntimeDesktop,
			State: agent.CodexThreadState{
				ThreadID: "thread-b", LastTurnID: "turn-before-binding", LastTurnStatus: "completed",
			},
		},
		nil,
	)
	if !baseline.initialized || baseline.pending || baseline.turnID != "turn-before-binding" {
		t.Fatalf("baseline=%#v, want authoritative runtime terminal state", baseline)
	}
}

func TestCodexFollowerBaselineFallsBackToRolloutWhenRuntimeIsUnknown(t *testing.T) {
	baseline := codexFollowerBaselineFromSources(
		codexRolloutTaskState{TurnID: "turn-rollout", Active: true},
		agent.CodexThreadBinding{Runtime: agent.CodexRuntimeUnknown},
		nil,
	)
	if !baseline.initialized || !baseline.pending || baseline.turnID != "turn-rollout" {
		t.Fatalf("baseline=%#v, want rollout fallback for unknown runtime", baseline)
	}
}

func TestCodexFollowerBaselineStartsEmptyWhenRuntimeIsTemporarilyUnavailable(t *testing.T) {
	baseline := codexFollowerBaselineFromSources(
		codexRolloutTaskState{},
		agent.CodexThreadBinding{Runtime: agent.CodexRuntimeUnknown},
		agent.ErrCodexRuntimeUnavailable,
	)
	if !baseline.initialized || baseline.pending || baseline.turnID != "" {
		t.Fatalf("unavailable empty baseline=%#v, want initialized empty cursor", baseline)
	}
}

func TestUnavailableTargetDoesNotInstallFollowerOrDeliverLaterTerminal(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}
	req := f.request("thread-b")
	req.platform = platform.PlatformFeishu
	req.accountID = "cli_a"
	req.reply = &codexFollowerRouteReplier{Replier: f.reply, route: route}
	f.ag.handoffErrors["thread-b"] = agent.ErrCodexRuntimeUnavailable

	_, err := f.h.acquireCodexSessionWithBindingLocked(req)
	if !errors.Is(err, agent.ErrCodexRuntimeUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey); ok {
		t.Fatalf("snapshot=%#v, unavailable target must not install a future-delivery follower", snapshot)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceA {
		t.Fatalf("active workspace=%q, want previous %q", active, f.workspaceA)
	}
}

func TestAcquireCodexSessionDoesNotPersistRecoveryForUncommittedTarget(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a",
	}
	reference := platform.DurableCommandResultReference{
		Kind: "test_command_result", TargetID: "switch-card", Title: "会话切换结果",
		Command: "/cx switch thread-b",
	}
	req := f.request("thread-b")
	req.platform = platform.PlatformFeishu
	req.accountID = "cli_a"
	req.reply = &codexRuntimeRecoveryReferenceReplier{
		codexFollowerRouteReplier: &codexFollowerRouteReplier{Replier: f.reply, route: route},
		reference:                 reference,
	}
	f.ag.handoffErrors["thread-b"] = agent.ErrCodexRuntimeUnavailable

	_, err := f.h.acquireCodexSessionWithBindingLocked(req)
	if !errors.Is(err, agent.ErrCodexRuntimeUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey); ok {
		t.Fatalf("snapshot=%#v, failed target must not keep a recovery result", snapshot)
	}
}

func TestAcquireCodexSessionDoesNotPersistRuntimeRecoveryResultWhenReady(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a",
	}
	reference := platform.DurableCommandResultReference{
		Kind: "test_command_result", TargetID: "switch-card", Title: "会话切换结果",
		Command: "/cx switch thread-b",
	}
	req := f.request("thread-b")
	req.platform = platform.PlatformFeishu
	req.accountID = "cli_a"
	req.reply = &codexRuntimeRecoveryReferenceReplier{
		codexFollowerRouteReplier: &codexFollowerRouteReplier{Replier: f.reply, route: route},
		reference:                 reference,
	}

	result, err := f.h.acquireCodexSessionWithBindingLocked(req)
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok || snapshot.Target.RuntimeRecoveryResult != nil {
		t.Fatalf("snapshot=%#v ok=%v, ready runtime must not enqueue a recovery result", snapshot, ok)
	}
}

func TestCodexSwitchCommandRendersBindingSemantics(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	text := f.h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: f.routeUser, agentName: "codex",
		workspaceRoot: f.workspaceB, agent: f.ag, target: "thread-b",
		options: codexSwitchOptions{
			actorUserID: f.routeUser, authorizedIdentity: f.routeUser,
			platform: platform.PlatformFeishu, accountID: "cli_a", reply: f.reply,
		},
	})
	if !strings.Contains(text, "已切换并绑定") ||
		strings.Contains(text, "窗口绑定") || strings.Contains(text, "运行位置") ||
		strings.Contains(text, "控制方") || strings.Contains(text, "接管") {
		t.Fatalf("text=%q", text)
	}
}

func TestRenderCodexSessionAcquireResultExplainsDeferredDesktopAdoption(t *testing.T) {
	h := NewHandler(nil, nil)
	result := codexSessionAcquireResult{
		route:      codexConversationRoute{workspaceRoot: "/workspace/project", threadID: "thread-1"},
		runtimeErr: fmt.Errorf("%w: %w", agent.ErrCodexDesktopAdoptionDeferred, agent.ErrCodexWriterBusy),
	}

	text := h.renderCodexSessionAcquireSuccess(result)
	if !strings.Contains(text, "正在等待当前 WeClaw 任务结束") ||
		!strings.Contains(text, "任务结束后会自动接入 Codex App") {
		t.Fatalf("text=%q, want deferred Desktop adoption guidance", text)
	}
}

func TestRenderCodexSessionAcquireFailureExplainsOversizedProtocolFrame(t *testing.T) {
	err := fmt.Errorf("绑定恢复失败: %w", agent.ErrACPFrameTooLarge)
	text := renderCodexSessionAcquireFailure(err)
	if !strings.Contains(text, "单条会话数据过大") || !strings.Contains(text, "Codex CLI") {
		t.Fatalf("rendered failure=%q, want actionable oversized-frame explanation", text)
	}
}

func TestRenderCodexSessionAcquireResultExplainsDesktopConnectionRecovery(t *testing.T) {
	h := NewHandler(nil, nil)
	result := codexSessionAcquireResult{
		route:      codexConversationRoute{workspaceRoot: "/workspace/project", threadID: "thread-1"},
		runtimeErr: agent.ErrCodexDesktopOwnershipUnknown,
	}

	text := h.renderCodexSessionAcquireSuccess(result)
	if !strings.Contains(text, "正在接入 Codex App") ||
		!strings.Contains(text, "恢复后本卡会自动更新") {
		t.Fatalf("text=%q, want Desktop connection recovery guidance", text)
	}
}

func TestRenderCodexSessionAcquireResultKeepsProgressInDedicatedTaskCard(t *testing.T) {
	h := NewHandler(nil, nil)
	result := codexSessionAcquireResult{
		route:                codexConversationRoute{workspaceRoot: "/workspace/card-manager-android", threadID: "thread-active"},
		externalActive:       true,
		externalProgressCard: true,
		externalState: externalCodexTaskState{
			CodexThreadState: agent.CodexThreadState{Preview: "好，你推进吧"},
			Progress:         "正在精简活动卡片",
		},
	}

	text := h.renderCodexSessionAcquireSuccess(result)
	for _, want := range []string{
		"已切换并绑定", "工作空间: card-manager-android",
		"模型: 未知（会话未记录） · 推理强度: 未知（会话未记录）",
		"运行中任务: 进度和结果见下方任务卡",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text=%q, want %q", text, want)
		}
	}
	for _, duplicate := range []string{
		"窗口绑定:", "运行位置:", "共享 Codex 任务正在进行", "任务: 好，你推进吧",
		"当前进展:", "任务完成后结果会自动返回",
	} {
		if strings.Contains(text, duplicate) {
			t.Fatalf("text=%q, should not repeat %q", text, duplicate)
		}
	}
}

func TestRenderCodexSessionAcquireResultExplainsReanchoredTaskCard(t *testing.T) {
	h := NewHandler(nil, nil)
	result := codexSessionAcquireResult{
		route:                codexConversationRoute{workspaceRoot: "/workspace/card-manager-android", threadID: "thread-active"},
		externalActive:       true,
		externalProgressCard: true,
		progressReanchored:   true,
	}

	text := h.renderCodexSessionAcquireSuccess(result)
	if !strings.Contains(text, "运行中任务: 已移到当前消息底部继续更新") {
		t.Fatalf("text=%q, want reanchored task card notice", text)
	}
	for _, duplicate := range []string{"共享 Codex 任务正在进行", "\n\n任务:", "当前进展:"} {
		if strings.Contains(text, duplicate) {
			t.Fatalf("text=%q, should not repeat %q", text, duplicate)
		}
	}
}

func TestAcquireCodexSessionTargetLockTimeoutKeepsBinding(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.h.codexLockWaitTimeout = 20 * time.Millisecond
	unlock := f.h.lockCodexThreadControl("thread-b")
	defer unlock()
	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if !isCodexSessionControlTimeout(err) {
		t.Fatalf("error=%v", err)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceA {
		t.Fatalf("binding changed to %q", active)
	}
}
