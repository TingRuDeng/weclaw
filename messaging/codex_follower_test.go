package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexFollowerFailureLoggingUsesPowerOfTwoSampling(t *testing.T) {
	service := &codexFollowerService{}
	snapshot := codexFollowerSnapshot{BindingKey: "route", Target: codexFrontendFollower{ThreadID: "thread-1"}}
	err := agent.ErrCodexWriterBusy
	for index := 0; index < 3; index++ {
		service.recordReconcileResult(snapshot, err, nil)
	}
	state := service.failures["route\x00thread-1"]
	if state.count != 3 || state.summary != err.Error() {
		t.Fatalf("failure state=%#v", state)
	}
	service.recordReconcileResult(snapshot, nil, nil)
	if _, ok := service.failures["route\x00thread-1"]; ok {
		t.Fatal("successful reconcile did not clear sampled failure state")
	}
}

type codexFollowerTestPlatform struct {
	name    platform.PlatformName
	account string
	reply   platform.Replier
	replies map[platform.DeliveryRoute]platform.Replier
}

type codexFollowerDurableReplier struct {
	*platformtest.Replier
	route    platform.DeliveryRoute
	mu       sync.Mutex
	accepted map[string]bool
}

type codexFollowerCommandResultReplier struct {
	*platformtest.Replier
	mu         sync.Mutex
	references []platform.DurableCommandResultReference
	texts      []string
	err        error
}

func (r *codexFollowerCommandResultReplier) DeliverCommandResult(_ context.Context, reference platform.DurableCommandResultReference, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.references = append(r.references, reference)
	r.texts = append(r.texts, text)
	return r.err
}

func newCodexFollowerDurableReplier(route platform.DeliveryRoute, caps platform.Capabilities) *codexFollowerDurableReplier {
	return &codexFollowerDurableReplier{
		Replier: platformtest.NewReplier(caps), route: route, accepted: make(map[string]bool),
	}
}

func (r *codexFollowerDurableReplier) DeliveryRoute() platform.DeliveryRoute { return r.route }

func (r *codexFollowerDurableReplier) SendTextIdempotent(ctx context.Context, text string, key string) error {
	r.mu.Lock()
	if r.accepted[key] {
		r.mu.Unlock()
		return nil
	}
	r.accepted[key] = true
	r.mu.Unlock()
	return r.SendText(ctx, text)
}

func (r *codexFollowerDurableReplier) SendResultIdempotent(ctx context.Context, result platform.TerminalResult, key string) error {
	return r.SendTextIdempotent(ctx, result.Text, key)
}

func (p *codexFollowerTestPlatform) Name() platform.PlatformName { return p.name }
func (p *codexFollowerTestPlatform) AccountID() string           { return p.account }
func (p *codexFollowerTestPlatform) Capabilities() platform.Capabilities {
	return p.reply.Capabilities()
}
func (p *codexFollowerTestPlatform) Run(ctx context.Context, _ platform.DispatchFunc) error {
	<-ctx.Done()
	return nil
}
func (p *codexFollowerTestPlatform) NewReplier(chatID string) platform.Replier { return p.reply }

func (p *codexFollowerTestPlatform) NewReplierForRoute(route platform.DeliveryRoute) platform.Replier {
	if reply := p.replies[route]; reply != nil {
		return reply
	}
	return p.reply
}

type codexFollowerWatchAgent struct {
	*fakeCodexLiveAgent
	watchStarted    chan string
	activityMu      sync.Mutex
	activityHandler func(string)
}

type codexFollowerStructuredWatchAgent struct {
	*codexFollowerWatchAgent
	progress agent.ProgressEvent
}

type codexFollowerBroadcastWatchAgent struct {
	*codexFollowerWatchAgent
	mu        sync.Mutex
	next      int
	observers map[int]chan agent.ProgressEvent
	done      chan struct{}
	final     string
}

func (a *codexFollowerBroadcastWatchAgent) WatchCodexThreadEvents(
	ctx context.Context,
	_ string,
	threadID string,
	onProgress func(agent.ProgressEvent),
) (string, error) {
	a.mu.Lock()
	if a.observers == nil {
		a.observers = make(map[int]chan agent.ProgressEvent)
	}
	a.next++
	id := a.next
	events := make(chan agent.ProgressEvent, 8)
	a.observers[id] = events
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.observers, id)
		a.mu.Unlock()
	}()
	a.watchStarted <- threadID
	for {
		select {
		case event := <-events:
			if onProgress != nil {
				onProgress(event)
			}
		case <-a.done:
			return a.final, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (a *codexFollowerBroadcastWatchAgent) emit(event agent.ProgressEvent) {
	a.mu.Lock()
	observers := make([]chan agent.ProgressEvent, 0, len(a.observers))
	for _, observer := range a.observers {
		observers = append(observers, observer)
	}
	a.mu.Unlock()
	for _, observer := range observers {
		observer <- event
	}
}

func (a *codexFollowerStructuredWatchAgent) WatchCodexThreadEvents(
	ctx context.Context,
	_ string,
	threadID string,
	onProgress func(agent.ProgressEvent),
) (string, error) {
	if onProgress != nil {
		onProgress(a.progress)
	}
	a.watchStarted <- threadID
	if a.fakeCodexThreadAgent.watchDone != nil {
		select {
		case <-a.fakeCodexThreadAgent.watchDone:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return a.fakeCodexThreadAgent.watchReply, a.fakeCodexThreadAgent.watchErr
}

func (a *codexFollowerWatchAgent) WatchCodexThread(ctx context.Context, conversationID string, threadID string, onProgress func(string)) (string, error) {
	a.watchStarted <- threadID
	return a.fakeCodexLiveAgent.WatchCodexThread(ctx, conversationID, threadID, onProgress)
}

func (a *codexFollowerWatchAgent) SetCodexThreadActivityHandler(handler func(string)) {
	a.activityMu.Lock()
	a.activityHandler = handler
	a.activityMu.Unlock()
}

func (a *codexFollowerWatchAgent) notifyCodexThreadActivity(threadID string) {
	a.activityMu.Lock()
	handler := a.activityHandler
	a.activityMu.Unlock()
	if handler != nil {
		handler(threadID)
	}
}

type codexFollowerRuntimeBootstrapAgent struct {
	*codexFollowerWatchAgent
	mu    sync.Mutex
	ready bool
}

type codexFollowerHostTopologyAgent struct {
	*codexFollowerWatchAgent
	mu         sync.Mutex
	reconciled bool
}

func (a *codexFollowerHostTopologyAgent) ReconcileCodexHostTopology(context.Context) error {
	a.mu.Lock()
	a.reconciled = true
	a.mu.Unlock()
	return nil
}

func (a *codexFollowerHostTopologyAgent) topologyReconciled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reconciled
}

func TestEnsureCodexFollowerRuntimeReconcilesHostBeforeAcceptingExistingBinding(t *testing.T) {
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-1"})
	ag := &codexFollowerHostTopologyAgent{
		codexFollowerWatchAgent: &codexFollowerWatchAgent{
			fakeCodexLiveAgent: base,
			watchStarted:       make(chan string, 1),
		},
	}
	request := agent.CodexRuntimeRequest{
		Ref: agent.CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
	}

	err := ensureCodexFollowerRuntime(context.Background(), ag, request)

	if err != nil || !ag.topologyReconciled() {
		t.Fatalf("error=%v reconciled=%v", err, ag.topologyReconciled())
	}
}

func (a *codexFollowerRuntimeBootstrapAgent) CurrentCodexRuntime(req agent.CodexRuntimeRequest) (agent.CodexThreadBinding, error) {
	a.mu.Lock()
	ready := a.ready
	a.mu.Unlock()
	binding, err := a.codexFollowerWatchAgent.CurrentCodexRuntime(req)
	if !ready {
		binding.Runtime = agent.CodexRuntimeUnknown
	}
	return binding, err
}

func (a *codexFollowerRuntimeBootstrapAgent) HandoffCodexRuntime(ctx context.Context, req agent.CodexRuntimeRequest) (agent.CodexThreadBinding, error) {
	a.mu.Lock()
	a.ready = true
	a.mu.Unlock()
	return a.codexFollowerWatchAgent.HandoffCodexRuntime(ctx, req)
}

func (a *codexFollowerRuntimeBootstrapAgent) ReadCodexThreadState(ctx context.Context, conversationID string, threadID string) (agent.CodexThreadState, error) {
	a.mu.Lock()
	ready := a.ready
	a.mu.Unlock()
	if !ready {
		return agent.CodexThreadState{}, agent.ErrCodexRuntimeUnavailable
	}
	return a.codexFollowerWatchAgent.ReadCodexThreadState(ctx, conversationID, threadID)
}

func TestCodexFollowerReestablishesRuntimeBeforeReadingRestartedDesktopState(t *testing.T) {
	h, ag, registry, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	bootstrap := &codexFollowerRuntimeBootstrapAgent{codexFollowerWatchAgent: ag}
	h.SetDefaultAgent("codex", bootstrap)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond)

	select {
	case threadID := <-ag.watchStarted:
		if threadID != snapshot.Target.ThreadID {
			t.Fatalf("watched thread=%q, want %q", threadID, snapshot.Target.ThreadID)
		}
	case <-time.After(time.Second):
		t.Fatal("persisted follower did not reestablish the runtime before reading thread state")
	}
	h.detachCodexFrontendTask(snapshot.ConversationID, snapshot.RouteUserID, snapshot.Target.ThreadID)
	cancel()
	waitForRolloverCondition(t, func() bool {
		h.codexFollowerMu.Lock()
		defer h.codexFollowerMu.Unlock()
		return h.codexFollower == nil
	})
	close(watchDone)
}

func TestCodexFollowerRecoveryUpdatesPendingSwitchResult(t *testing.T) {
	h, _, _, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
	})
	defer close(watchDone)
	reference := platform.DurableCommandResultReference{
		Kind: "test_command_result", TargetID: "switch-card", Title: "会话切换结果",
		Command: "/cx switch thread-local",
	}
	store := h.ensureCodexSessions()
	store.mu.Lock()
	binding := store.bindings[snapshot.BindingKey]
	binding.Follower.RuntimeRecoveryResult = &reference
	store.bindings[snapshot.BindingKey] = binding
	store.mu.Unlock()
	store.save()
	snapshot = store.followerSnapshots()[0]

	reply := &codexFollowerCommandResultReplier{Replier: platformtest.NewReplier(platform.Capabilities{Text: true})}
	registry := newCodexFollowerTestRegistry(snapshot.Target.DeliveryRoute, reply)
	h.reconcileCodexFollowersWithService(context.Background(), &codexFollowerService{
		registry: registry, failures: make(map[string]codexFollowerFailureState),
	})

	reply.mu.Lock()
	references := append([]platform.DurableCommandResultReference(nil), reply.references...)
	texts := append([]string(nil), reply.texts...)
	reply.mu.Unlock()
	if len(references) != 1 || references[0] != reference {
		t.Fatalf("references=%#v, want original switch-card reference", references)
	}
	if len(texts) != 1 || !strings.Contains(texts[0], "已切换并绑定") ||
		!strings.Contains(texts[0], "运行通道: 已恢复") {
		t.Fatalf("texts=%#v, want recovered switch result", texts)
	}
	current, ok := store.followerSnapshot(snapshot.BindingKey)
	if !ok || current.Target.RuntimeRecoveryResult != nil {
		t.Fatalf("current=%#v ok=%v, recovered result must be cleared after delivery", current, ok)
	}
}

func TestCodexFollowerRecoveryWaitsUntilInitialSwitchCardCanNoLongerOverwriteIt(t *testing.T) {
	h, _, _, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
	})
	defer close(watchDone)
	reference := platform.DurableCommandResultReference{
		Kind: "test_command_result", TargetID: "switch-card", Title: "会话切换结果",
		Command: "/cx switch thread-local", ReadyAfter: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
	}
	store := h.ensureCodexSessions()
	store.mu.Lock()
	binding := store.bindings[snapshot.BindingKey]
	binding.Follower.RuntimeRecoveryResult = &reference
	store.bindings[snapshot.BindingKey] = binding
	store.mu.Unlock()
	store.save()

	reply := &codexFollowerCommandResultReplier{Replier: platformtest.NewReplier(platform.Capabilities{Text: true})}
	registry := newCodexFollowerTestRegistry(snapshot.Target.DeliveryRoute, reply)
	h.reconcileCodexFollowersWithService(context.Background(), &codexFollowerService{registry: registry})
	reply.mu.Lock()
	attempts := len(reply.references)
	reply.mu.Unlock()
	if attempts != 0 {
		t.Fatalf("card attempts=%d before initial callback settles, want 0", attempts)
	}
	current, ok := store.followerSnapshot(snapshot.BindingKey)
	if !ok || current.Target.RuntimeRecoveryResult == nil {
		t.Fatalf("current=%#v ok=%v, deferred result must remain pending", current, ok)
	}
	store.mu.Lock()
	binding = store.bindings[snapshot.BindingKey]
	pastReference := *binding.Follower.RuntimeRecoveryResult
	pastReference.ReadyAfter = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	binding.Follower.RuntimeRecoveryResult = &pastReference
	store.bindings[snapshot.BindingKey] = binding
	store.mu.Unlock()
	store.save()
	h.reconcileCodexFollowersWithService(context.Background(), &codexFollowerService{registry: registry})
	reply.mu.Lock()
	attempts = len(reply.references)
	reply.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("card attempts=%d after callback grace period, want 1", attempts)
	}
	current, ok = store.followerSnapshot(snapshot.BindingKey)
	if !ok || current.Target.RuntimeRecoveryResult != nil {
		t.Fatalf("current=%#v ok=%v, delivered result must be cleared", current, ok)
	}
}

func TestCodexFollowerRecoveryKeepsPendingSwitchResultUntilRuntimeAndCardAreReady(t *testing.T) {
	h, ag, _, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
	})
	defer close(watchDone)
	reference := platform.DurableCommandResultReference{
		Kind: "test_command_result", TargetID: "switch-card", Title: "会话切换结果",
		Command: "/cx switch thread-local",
	}
	store := h.ensureCodexSessions()
	store.mu.Lock()
	binding := store.bindings[snapshot.BindingKey]
	binding.Follower.RuntimeRecoveryResult = &reference
	store.bindings[snapshot.BindingKey] = binding
	store.mu.Unlock()
	store.save()

	reply := &codexFollowerCommandResultReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true}),
		err:     errors.New("card patch unavailable"),
	}
	registry := newCodexFollowerTestRegistry(snapshot.Target.DeliveryRoute, reply)
	service := &codexFollowerService{
		registry: registry, failures: make(map[string]codexFollowerFailureState),
		resultFailures: make(map[string]codexFollowerFailureState),
	}

	ag.fakeCodexThreadAgent.threadStateErr = errors.New("desktop state unavailable")
	h.reconcileCodexFollowersWithService(context.Background(), service)
	reply.mu.Lock()
	firstAttempts := len(reply.references)
	reply.mu.Unlock()
	if firstAttempts != 0 {
		t.Fatalf("card update attempts=%d while runtime is unavailable, want 0", firstAttempts)
	}
	current, ok := store.followerSnapshot(snapshot.BindingKey)
	if !ok || current.Target.RuntimeRecoveryResult == nil {
		t.Fatalf("current=%#v ok=%v, runtime failure must keep pending result", current, ok)
	}

	ag.fakeCodexThreadAgent.threadStateErr = nil
	h.reconcileCodexFollowersWithService(context.Background(), service)
	current, ok = store.followerSnapshot(snapshot.BindingKey)
	if !ok || current.Target.RuntimeRecoveryResult == nil {
		t.Fatalf("current=%#v ok=%v, card failure must keep pending result", current, ok)
	}
	reply.mu.Lock()
	if len(reply.references) != 1 {
		reply.mu.Unlock()
		t.Fatalf("card attempts=%d, want one failed attempt", len(reply.references))
	}
	reply.err = nil
	reply.mu.Unlock()

	h.reconcileCodexFollowersWithService(context.Background(), service)
	reply.mu.Lock()
	attempts := len(reply.references)
	reply.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("card attempts=%d, want retry after patch failure", attempts)
	}
	current, ok = store.followerSnapshot(snapshot.BindingKey)
	if !ok || current.Target.RuntimeRecoveryResult != nil {
		t.Fatalf("current=%#v ok=%v, successful retry must clear pending result", current, ok)
	}
}

func TestCodexFollowerRecoveryResultSurvivesRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	first, _, _, snapshot, firstWatchDone := newCodexFollowerFixtureWithStatePath(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
	}, statePath)
	close(firstWatchDone)
	reference := platform.DurableCommandResultReference{
		Kind: "test_command_result", TargetID: "switch-card", Title: "会话切换结果",
		Command: "/cx switch thread-local",
	}
	store := first.ensureCodexSessions()
	store.mu.Lock()
	binding := store.bindings[snapshot.BindingKey]
	binding.Follower.RuntimeRecoveryResult = &reference
	store.bindings[snapshot.BindingKey] = binding
	store.mu.Unlock()
	store.save()

	second := NewHandler(nil, nil)
	second.SetCodexSessionFile(statePath)
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
	})
	watchDone := make(chan struct{})
	base.watchDone = watchDone
	defer close(watchDone)
	second.SetDefaultAgent("codex", &codexFollowerWatchAgent{
		fakeCodexLiveAgent: base, watchStarted: make(chan string, 1),
	})
	loaded := second.ensureCodexSessions().followerSnapshots()
	if len(loaded) != 1 || loaded[0].Target.RuntimeRecoveryResult == nil {
		t.Fatalf("loaded=%#v, want persisted pending result", loaded)
	}
	reply := &codexFollowerCommandResultReplier{Replier: platformtest.NewReplier(platform.Capabilities{Text: true})}
	registry := newCodexFollowerTestRegistry(loaded[0].Target.DeliveryRoute, reply)
	second.reconcileCodexFollowersWithService(context.Background(), &codexFollowerService{registry: registry})
	reply.mu.Lock()
	attempts := len(reply.references)
	reply.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("card attempts=%d after restart, want 1", attempts)
	}
	current, ok := second.ensureCodexSessions().followerSnapshot(loaded[0].BindingKey)
	if !ok || current.Target.RuntimeRecoveryResult != nil {
		t.Fatalf("current=%#v ok=%v, restart recovery must clear pending result", current, ok)
	}
}

func TestCodexFollowerAttachesWhenBoundThreadStartsWithoutInboundMessage(t *testing.T) {
	h, ag, registry, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond)

	time.Sleep(30 * time.Millisecond)
	if _, active := h.activeTask(snapshot.ConversationID); active {
		t.Fatal("idle bound thread unexpectedly created an observer")
	}
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	select {
	case threadID := <-ag.watchStarted:
		if threadID != "thread-local" {
			t.Fatalf("watched thread=%q", threadID)
		}
	case <-time.After(time.Second):
		t.Fatal("follower did not attach after the local turn started")
	}
	task, active := h.activeTask(snapshot.ConversationID)
	if !active {
		t.Fatal("follower observer is not registered as the active delivery task")
	}
	_ = task

	result := h.detachCodexFrontendTask(snapshot.ConversationID, snapshot.RouteUserID, snapshot.Target.ThreadID)
	if !result.detached {
		t.Fatal("failed to detach follower test observer")
	}
	close(watchDone)
}

func TestCodexFollowerRevocationDetachesActiveObserver(t *testing.T) {
	h, ag, _, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &codexFollowerTestPlatform{
			name: snapshot.Target.DeliveryRoute.Platform, account: snapshot.Target.DeliveryRoute.AccountID, reply: reply,
		},
		Access: platform.NewAccessControl([]string{"user"}),
	}})
	h.SetPlatformRegistry(registry)
	t.Cleanup(func() {
		select {
		case <-watchDone:
		default:
			close(watchDone)
		}
	})
	if err := h.reconcileCodexFollower(context.Background(), registry, snapshot); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ag.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("authorized follower did not attach its observer")
	}
	if _, active := h.activeTask(snapshot.ConversationID); !active {
		t.Fatal("authorized follower observer was not registered")
	}

	h.refreshFeishuAccountAccess(snapshot.Target.DeliveryRoute.AccountID, nil)
	if _, active := h.activeTask(snapshot.ConversationID); active {
		t.Fatal("revoke returned while follower observer still had outbound delivery capability")
	}
	store := h.ensureCodexSessions()
	store.mu.Lock()
	binding := store.bindings[snapshot.BindingKey]
	store.mu.Unlock()
	if binding.Follower != nil || binding.FollowRevision <= snapshot.Revision {
		t.Fatalf("revoke did not durably clear follower: %#v", binding)
	}
}

func TestCodexFollowerWaitsForBindingBeforeTakingDeliveryBarrier(t *testing.T) {
	h, _, registry, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	defer close(watchDone)
	unlockBinding, err := h.lockCodexSessionBinding(context.Background(), snapshot.BindingKey, "test-release")
	if err != nil {
		t.Fatal(err)
	}
	reconcileDone := make(chan error, 1)
	go func() {
		reconcileDone <- h.reconcileCodexFollower(context.Background(), registry, snapshot)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		h.taskLocksMu.Lock()
		lock := h.taskLocks[codexBindingExecutionKey(snapshot.BindingKey)]
		waiters := 0
		if lock != nil {
			waiters = lock.users
		}
		h.taskLocksMu.Unlock()
		if waiters >= 2 {
			break
		}
		if time.Now().After(deadline) {
			unlockBinding()
			t.Fatal("follower did not begin waiting for the binding lock")
		}
		time.Sleep(time.Millisecond)
	}

	barrierAcquired := make(chan struct{})
	releaseBarrier := make(chan struct{})
	go func() {
		h.codexFollowerDeliveryMu.Lock()
		close(barrierAcquired)
		<-releaseBarrier
		h.codexFollowerDeliveryMu.Unlock()
	}()
	select {
	case <-barrierAcquired:
		close(releaseBarrier)
		unlockBinding()
	case <-time.After(100 * time.Millisecond):
		unlockBinding()
		<-barrierAcquired
		close(releaseBarrier)
		t.Fatal("follower held the delivery barrier while waiting for the binding lock")
	}
	if err := <-reconcileDone; err != nil {
		t.Fatal(err)
	}
}

func TestCodexFollowerActivityWakeAttachesBeforePeriodicReconcile(t *testing.T) {
	h, ag, registry, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
	})
	initialRead := make(chan struct{}, 1)
	releaseInitialRead := make(chan struct{})
	ag.fakeCodexThreadAgent.threadStateEntered = initialRead
	ag.fakeCodexThreadAgent.threadStateRelease = releaseInitialRead
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startCodexFollowerReconciler(ctx, registry, time.Hour)
	select {
	case <-initialRead:
	case <-time.After(time.Second):
		t.Fatal("initial idle follower reconciliation did not start")
	}
	close(releaseInitialRead)
	waitForRolloverCondition(t, func() bool {
		_, active := h.activeTask(snapshot.ConversationID)
		return !active
	})
	time.Sleep(20 * time.Millisecond)

	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	ag.notifyCodexThreadActivity("thread-local")
	select {
	case threadID := <-ag.watchStarted:
		if threadID != snapshot.Target.ThreadID {
			t.Fatalf("watched thread=%q, want %q", threadID, snapshot.Target.ThreadID)
		}
	case <-time.After(time.Second):
		t.Fatal("thread activity did not trigger follower reconciliation")
	}
	h.detachCodexFrontendTask(snapshot.ConversationID, snapshot.RouteUserID, snapshot.Target.ThreadID)
	close(watchDone)
}

func TestCodexFollowerInitialIdleBindingBaselinesHistoricalTurn(t *testing.T) {
	h, _, registry, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
		LastTurnID: "turn-before-binding", LastTurnStatus: "completed",
		LastAgentMessageText: "绑定前的历史结果",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond)
	waitForRolloverCondition(t, func() bool {
		current := h.ensureCodexSessions().followerSnapshots()
		return len(current) == 1 && current[0].FollowTurnInitialized &&
			current[0].FollowTurnID == "turn-before-binding"
	})
	if _, active := h.activeTask(snapshot.ConversationID); active {
		t.Fatal("historical idle turn unexpectedly created an observer")
	}
	close(watchDone)
}

func TestCodexFollowerCatchesUpTurnCompletedBeforeObserverAttach(t *testing.T) {
	h, ag, _, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
		LastTurnID: "turn-before-binding", LastTurnStatus: "completed",
	})
	reply := newOutboxTestReplier(snapshot.Target.DeliveryRoute)
	reply.finalOutside = true
	registry := newOutboxTestRegistry(snapshot.Target.DeliveryRoute, reply)
	ctx, cancel := context.WithCancel(context.Background())
	outbox, err := newTerminalOutbox(filepath.Join(t.TempDir(), "terminal-outbox.json"), registry)
	if err != nil {
		t.Fatal(err)
	}
	h.terminalOutboxMu.Lock()
	h.terminalOutbox = outbox
	h.terminalOutboxMu.Unlock()
	if err := h.startCodexFollowerReconciler(ctx, registry, time.Hour); err != nil {
		t.Fatal(err)
	}
	waitForRolloverCondition(t, func() bool {
		current := h.ensureCodexSessions().followerSnapshots()
		return len(current) == 1 && current[0].FollowTurnInitialized
	})

	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
		LastTurnID: "turn-too-fast", LastTurnStatus: "completed",
		LastAgentMessageText: "快速任务的最终回答",
	})
	ag.notifyCodexThreadActivity("thread-local")
	waitForRolloverCondition(t, func() bool {
		current := h.ensureCodexSessions().followerSnapshots()
		return len(current) == 1 && current[0].FollowTurnID == "turn-too-fast"
	})
	id := codexFollowerTerminalOutboxID(snapshot, "turn-too-fast")
	if err := outbox.attempt(context.Background(), id, reply); err != nil {
		t.Fatal(err)
	}
	waitForRolloverCondition(t, func() bool {
		reply.mu.Lock()
		defer reply.mu.Unlock()
		return len(reply.results) == 1
	})
	reply.mu.Lock()
	result := reply.results[0]
	reply.mu.Unlock()
	if result.Text != "快速任务的最终回答" || result.State != platform.StreamTerminalCompleted {
		t.Fatalf("catch-up result=%#v", result)
	}
	current := h.ensureCodexSessions().followerSnapshots()
	if len(current) != 1 || current[0].FollowTurnID != "turn-too-fast" {
		t.Fatalf("follower cursor=%#v", current)
	}
	select {
	case <-ag.watchStarted:
		t.Fatal("completed quick turn unexpectedly started a watcher")
	default:
	}
	cancel()
	waitForRolloverCondition(t, func() bool {
		h.codexFollowerMu.Lock()
		defer h.codexFollowerMu.Unlock()
		return h.codexFollower == nil
	})
	close(watchDone)
}

func TestCodexFollowerTerminalKeyIgnoresReplyAnchorButSeparatesBindings(t *testing.T) {
	base := codexFollowerSnapshot{
		BindingKey: "binding-a", AgentName: "codex",
		Target: codexFrontendFollower{
			ThreadID: "thread-1", AuthorizedIdentity: "union-a",
			DeliveryRoute: platform.DeliveryRoute{
				Platform: platform.PlatformFeishu, AccountID: "bot-a", ChatID: "chat-a", ReplyToID: "message-1",
			},
		},
	}
	first := codexFollowerTerminalOutboxID(base, "turn-1")
	reanchored := base
	reanchored.Target.DeliveryRoute.ReplyToID = "message-2"
	if got := codexFollowerTerminalOutboxID(reanchored, "turn-1"); got != first {
		t.Fatalf("reply anchor changed terminal key: got=%s want=%s", got, first)
	}
	secondBinding := base
	secondBinding.BindingKey = "binding-b"
	if got := codexFollowerTerminalOutboxID(secondBinding, "turn-1"); got == first {
		t.Fatalf("different frontend bindings collided on terminal key %s", got)
	}
}

func TestCodexFollowerRecoversTerminalAfterCrashBetweenClaimAndActivation(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	first, _, _, snapshot, watchDone := newCodexFollowerFixtureWithStatePath(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	}, statePath)
	defer close(watchDone)

	// 模拟 active turn 游标已落盘，但 watcher 和 durable outbox 尚未建立时进程退出。
	if err := first.ensureCodexSessions().commitFollowerTurnPending(snapshot, "turn-local-1"); err != nil {
		t.Fatal(err)
	}

	second := NewHandler(nil, nil)
	second.SetCodexSessionFile(statePath)
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID:             "thread-local",
		Active:               false,
		LastTurnID:           "turn-local-1",
		LastTurnStatus:       "completed",
		LastAgentMessageText: "停机期间完成的最终回答",
	})
	second.SetDefaultAgent("codex", &codexFollowerWatchAgent{
		fakeCodexLiveAgent: base,
		watchStarted:       make(chan string, 1),
	})
	loaded := second.ensureCodexSessions().followerSnapshots()[0]
	reply := newOutboxTestReplier(loaded.Target.DeliveryRoute)
	registry := newOutboxTestRegistry(loaded.Target.DeliveryRoute, reply)
	outbox, err := newTerminalOutbox(filepath.Join(t.TempDir(), "terminal-outbox.json"), registry)
	if err != nil {
		t.Fatal(err)
	}
	second.terminalOutboxMu.Lock()
	second.terminalOutbox = outbox
	second.terminalOutboxMu.Unlock()

	if err := second.reconcileCodexFollower(context.Background(), registry, loaded); err != nil {
		t.Fatal(err)
	}
	id := codexFollowerTerminalOutboxID(loaded, "turn-local-1")
	outbox.mu.Lock()
	entry := outbox.entryLocked(id)
	outbox.mu.Unlock()
	if entry == nil {
		t.Fatal("active follower claim without a durable observer caused terminal delivery to be skipped")
	}
}

func TestCodexFollowerRearmsForSecondLocalTurn(t *testing.T) {
	h, ag, registry, snapshot, firstDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond)
	select {
	case <-ag.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("first local turn was not observed")
	}

	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-local", Active: false, LastTurnID: "turn-local-1", LastTurnStatus: "completed",
	})
	close(firstDone)
	waitForRolloverCondition(t, func() bool {
		_, active := h.activeTask(snapshot.ConversationID)
		return !active
	})
	secondDone := make(chan struct{})
	ag.fakeCodexThreadAgent.watchDone = secondDone
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-2",
	})
	select {
	case <-ag.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("follower did not rearm for the second local turn")
	}
	task, active := h.activeTask(snapshot.ConversationID)
	if !active {
		t.Fatal("second local turn observer is not active")
	}
	task.mu.Lock()
	turnID := task.codexTurnID
	task.mu.Unlock()
	if turnID != "turn-local-2" {
		t.Fatalf("observed turn=%q, want turn-local-2", turnID)
	}
	h.detachCodexFrontendTask(snapshot.ConversationID, snapshot.RouteUserID, snapshot.Target.ThreadID)
	close(secondDone)
}

func TestCodexFollowerReleaseDuringProbeStartsNoObserver(t *testing.T) {
	h, ag, registry, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	entered := make(chan struct{}, 1)
	releaseProbe := make(chan struct{})
	ag.fakeCodexThreadAgent.threadStateEntered = entered
	ag.fakeCodexThreadAgent.threadStateRelease = releaseProbe
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startCodexFollowerReconciler(ctx, registry, time.Hour)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("follower did not probe the bound thread")
	}
	if _, err := h.ensureCodexSessions().releaseWorkspaceThread(snapshot.BindingKey, snapshot.Target.WorkspaceRoot); err != nil {
		t.Fatal(err)
	}
	close(releaseProbe)
	time.Sleep(50 * time.Millisecond)
	if _, active := h.activeTask(snapshot.ConversationID); active {
		t.Fatal("stale follower probe attached after release")
	}
	select {
	case <-ag.watchStarted:
		t.Fatal("watcher started after follower revision changed")
	default:
	}
	close(watchDone)
}

func TestCodexFollowerRestoresFromPersistedBindingWithoutInboundMessage(t *testing.T) {
	statePath := t.TempDir() + "/codex-sessions.json"
	first, _, _, _, firstDone := newCodexFollowerFixtureWithStatePath(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	}, statePath)
	if len(first.ensureCodexSessions().followerSnapshots()) != 1 {
		t.Fatal("fixture follower was not persisted")
	}
	close(firstDone)

	second := NewHandler(nil, nil)
	second.SetCodexSessionFile(statePath)
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	base.watchDone = make(chan struct{})
	ag := &codexFollowerWatchAgent{fakeCodexLiveAgent: base, watchStarted: make(chan string, 2)}
	second.SetDefaultAgent("codex", ag)
	snapshots := second.ensureCodexSessions().followerSnapshots()
	if len(snapshots) != 1 {
		t.Fatalf("reloaded followers=%#v", snapshots)
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	registry := newCodexFollowerTestRegistry(snapshots[0].Target.DeliveryRoute, reply)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond)
	select {
	case <-ag.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("persisted follower did not restore after restart")
	}
	second.detachCodexFrontendTask(snapshots[0].ConversationID, snapshots[0].RouteUserID, snapshots[0].Target.ThreadID)
	close(base.watchDone)
}

func TestCodexFollowerFansOutSharedTurnToMultipleRoutes(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetCodexSessionFile(filepath.Join(t.TempDir(), "codex-sessions.json"))
	progressCfg := config.DefaultProgressConfig()
	progressCfg.Mode = progressModeStream
	progressCfg.InitialDelaySeconds = 0
	h.SetProgressConfig(progressCfg)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		string(platform.PlatformFeishu): {Mode: progressModeStream},
	})
	workspace := t.TempDir()
	routes := []platform.DeliveryRoute{
		{Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat-a", ReplyToID: "message-a"},
		{Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat-b", ReplyToID: "message-b"},
	}
	routeUsers := []string{"feishu:bot:dm:chat-a:user", "feishu:bot:dm:chat-b:user"}
	store := h.ensureCodexSessions()
	for index, routeUserID := range routeUsers {
		bindingKey := codexBindingKey(routeUserID, "codex")
		store.setActiveWorkspace(bindingKey, workspace)
		store.setThread(bindingKey, workspace, "thread-shared")
		store.mu.Lock()
		binding := store.bindings[bindingKey]
		binding.FollowRevision++
		binding.Follower = &codexFrontendFollower{
			WorkspaceRoot: workspace, ThreadID: "thread-shared", ActorUserID: "user", AuthorizedIdentity: "user",
			DeliveryRoute: routes[index], UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		store.bindings[bindingKey] = binding
		store.mu.Unlock()
	}
	store.save()

	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-shared", Active: true, ActiveTurnID: "turn-shared",
	})
	watchDone := make(chan struct{})
	base.watchDone = watchDone
	base.watchReply = "共享任务最终回答"
	ag := &codexFollowerStructuredWatchAgent{
		codexFollowerWatchAgent: &codexFollowerWatchAgent{
			fakeCodexLiveAgent: base, watchStarted: make(chan string, 4),
		},
		progress: agent.ProgressEvent{
			ID: "agent-message:shared-progress", Kind: agent.ProgressKindCommentary,
			State: agent.ProgressStateCompleted, Sequence: 1, Text: "正在分析共享任务。",
		},
	}
	h.SetDefaultAgent("codex", ag)
	replyCaps := platform.Capabilities{Text: true, Streaming: true, FinalReplyOutsideStream: true}
	replyA := newCodexFollowerDurableReplier(routes[0], replyCaps)
	replyB := newCodexFollowerDurableReplier(routes[1], replyCaps)
	platformImpl := &codexFollowerTestPlatform{
		name: platform.PlatformFeishu, account: "bot", reply: replyA,
		replies: map[platform.DeliveryRoute]platform.Replier{routes[0]: replyA, routes[1]: replyB},
	}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: platformImpl, Access: platform.NewAccessControl([]string{"user"}),
	}})
	h.SetPlatformRegistry(registry)
	outbox, err := newTerminalOutbox(filepath.Join(t.TempDir(), "terminal-outbox.json"), registry)
	if err != nil {
		t.Fatal(err)
	}
	h.terminalOutboxMu.Lock()
	h.terminalOutbox = outbox
	h.terminalOutboxMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	if err := h.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for range routes {
		select {
		case threadID := <-ag.watchStarted:
			if threadID != "thread-shared" {
				t.Fatalf("watched thread=%q", threadID)
			}
		case <-time.After(time.Second):
			t.Fatal("shared turn did not attach all frontend observers")
		}
	}
	for _, snapshot := range store.followerSnapshots() {
		if _, active := h.activeTask(snapshot.ConversationID); !active {
			t.Fatalf("route %q has no active delivery task", snapshot.RouteUserID)
		}
	}
	progressDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(progressDeadline) {
		if containsText(replyA.Stream.UpdatesSnapshot(), "正在分析共享任务") &&
			containsText(replyB.Stream.UpdatesSnapshot(), "正在分析共享任务") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !containsText(replyA.Stream.UpdatesSnapshot(), "正在分析共享任务") ||
		!containsText(replyB.Stream.UpdatesSnapshot(), "正在分析共享任务") {
		close(watchDone)
		cancel()
		t.Fatalf("progress route A=%#v route B=%#v", replyA.Stream.UpdatesSnapshot(), replyB.Stream.UpdatesSnapshot())
	}

	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-shared", Active: false,
		LastTurnID: "turn-shared", LastTurnStatus: "completed",
		LastAgentMessageText: "共享任务最终回答",
	})
	close(watchDone)
	resultDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(resultDeadline) {
		if countText(replyA.TextsSnapshot(), "共享任务最终回答") == 1 &&
			countText(replyB.TextsSnapshot(), "共享任务最终回答") == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if countText(replyA.TextsSnapshot(), "共享任务最终回答") != 1 ||
		countText(replyB.TextsSnapshot(), "共享任务最终回答") != 1 {
		t.Fatalf("route A=%#v route B=%#v", replyA.TextsSnapshot(), replyB.TextsSnapshot())
	}
	waitForRolloverCondition(t, func() bool {
		for _, snapshot := range store.followerSnapshots() {
			if _, active := h.activeTask(snapshot.ConversationID); active ||
				snapshot.FollowTurnID != "turn-shared" || snapshot.FollowTurnPending {
				return false
			}
		}
		return true
	})
	cancel()
	waitForRolloverCondition(t, func() bool {
		h.codexFollowerMu.Lock()
		defer h.codexFollowerMu.Unlock()
		return h.codexFollower == nil
	})
}

func TestCodexFollowerReleaseOneRouteKeepsOtherRouteDelivery(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetCodexSessionFile(filepath.Join(t.TempDir(), "codex-sessions.json"))
	workspace := t.TempDir()
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	progressCfg := config.DefaultProgressConfig()
	progressCfg.Mode = progressModeStream
	progressCfg.InitialDelaySeconds = 0
	h.SetProgressConfig(progressCfg)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		string(platform.PlatformFeishu): {Mode: progressModeStream, InitialDelaySeconds: 0},
	})
	routes := []platform.DeliveryRoute{
		{Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat-a", ReplyToID: "message-a"},
		{Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat-b", ReplyToID: "message-b"},
	}
	routeUsers := []string{"feishu:bot:dm:chat-a:user", "feishu:bot:dm:chat-b:user"}
	store := h.ensureCodexSessions()
	for index, routeUserID := range routeUsers {
		bindingKey := codexBindingKey(routeUserID, "codex")
		store.setActiveWorkspace(bindingKey, workspace)
		store.setThread(bindingKey, workspace, "thread-shared")
		store.mu.Lock()
		binding := store.bindings[bindingKey]
		binding.FollowRevision++
		binding.Follower = &codexFrontendFollower{
			WorkspaceRoot: workspace, ThreadID: "thread-shared", ActorUserID: "user", AuthorizedIdentity: "user",
			DeliveryRoute: routes[index], UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		store.bindings[bindingKey] = binding
		store.mu.Unlock()
	}
	store.save()

	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-shared", Active: true, ActiveTurnID: "turn-shared",
	})
	ag := &codexFollowerBroadcastWatchAgent{
		codexFollowerWatchAgent: &codexFollowerWatchAgent{
			fakeCodexLiveAgent: base, watchStarted: make(chan string, 4),
		},
		done: make(chan struct{}), final: "共享任务最终回答",
	}
	h.SetDefaultAgent("codex", ag)
	replyCaps := platform.Capabilities{Text: true, Streaming: true, FinalReplyOutsideStream: true}
	replyA := newCodexFollowerDurableReplier(routes[0], replyCaps)
	replyB := newCodexFollowerDurableReplier(routes[1], replyCaps)
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &codexFollowerTestPlatform{
			name: platform.PlatformFeishu, account: "bot", reply: replyA,
			replies: map[platform.DeliveryRoute]platform.Replier{routes[0]: replyA, routes[1]: replyB},
		},
		Access: platform.NewAccessControl([]string{"user"}),
	}})
	h.SetPlatformRegistry(registry)
	outbox, err := newTerminalOutbox(filepath.Join(t.TempDir(), "terminal-outbox.json"), registry)
	if err != nil {
		t.Fatal(err)
	}
	h.terminalOutboxMu.Lock()
	h.terminalOutbox = outbox
	h.terminalOutboxMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for range routes {
		select {
		case <-ag.watchStarted:
		case <-time.After(time.Second):
			t.Fatal("shared turn did not attach both frontend observers")
		}
	}

	ag.emit(agent.ProgressEvent{
		ID: "agent-message:before-release", Kind: agent.ProgressKindCommentary,
		State: agent.ProgressStateCompleted, Sequence: 1, Text: "释放前两端都可见。",
	})
	waitForRolloverCondition(t, func() bool {
		return containsText(replyA.Stream.UpdatesSnapshot(), "释放前两端都可见") &&
			containsText(replyB.Stream.UpdatesSnapshot(), "释放前两端都可见")
	})

	release := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user", RouteUserID: routeUsers[0],
		Trimmed: "/cx release", Platform: platform.PlatformFeishu, AccountID: "bot",
	})
	if !strings.Contains(release, "已解除当前窗口") {
		t.Fatalf("release result=%q", release)
	}
	waitForRolloverCondition(t, func() bool {
		_, activeA := h.activeTask(buildCodexConversationID(routeUsers[0], "codex", workspace))
		_, activeB := h.activeTask(buildCodexConversationID(routeUsers[1], "codex", workspace))
		return !activeA && activeB
	})

	ag.emit(agent.ProgressEvent{
		ID: "agent-message:after-release", Kind: agent.ProgressKindCommentary,
		State: agent.ProgressStateCompleted, Sequence: 2, Text: "释放后仅保留窗口可见。",
	})
	waitForRolloverCondition(t, func() bool {
		return containsText(replyB.Stream.UpdatesSnapshot(), "释放后仅保留窗口可见")
	})
	if containsText(replyA.Stream.UpdatesSnapshot(), "释放后仅保留窗口可见") {
		t.Fatalf("released route received late progress: %#v", replyA.Stream.UpdatesSnapshot())
	}

	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-shared", Active: false,
		LastTurnID: "turn-shared", LastTurnStatus: "completed",
		LastAgentMessageText: "共享任务最终回答",
	})
	close(ag.done)
	waitForRolloverCondition(t, func() bool {
		return countText(replyB.TextsSnapshot(), "共享任务最终回答") == 1
	})
	if countText(replyA.TextsSnapshot(), "共享任务最终回答") != 0 {
		t.Fatalf("released route received terminal=%#v", replyA.TextsSnapshot())
	}
	if base.interruptCalls != 0 {
		t.Fatalf("interrupt calls=%d, route release must not stop the shared turn", base.interruptCalls)
	}
	if threadID, pending := store.getThread(codexBindingKey(routeUsers[1], "codex"), workspace); threadID != "thread-shared" || pending {
		t.Fatalf("remaining route thread=%q pending=%v", threadID, pending)
	}
	waitForRolloverCondition(t, func() bool {
		snapshots := store.followerSnapshots()
		if len(snapshots) != 1 || snapshots[0].RouteUserID != routeUsers[1] ||
			snapshots[0].FollowTurnID != "turn-shared" || snapshots[0].FollowTurnPending {
			return false
		}
		_, active := h.activeTask(snapshots[0].ConversationID)
		return !active
	})
	cancel()
	waitForRolloverCondition(t, func() bool {
		h.codexFollowerMu.Lock()
		defer h.codexFollowerMu.Unlock()
		return h.codexFollower == nil
	})
}

func countText(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func newCodexFollowerFixture(t *testing.T, state agent.CodexThreadState) (*Handler, *codexFollowerWatchAgent, *platform.Registry, codexFollowerSnapshot, chan struct{}) {
	t.Helper()
	return newCodexFollowerFixtureWithStatePath(t, state, "")
}

func newCodexFollowerFixtureWithStatePath(t *testing.T, state agent.CodexThreadState, statePath string) (*Handler, *codexFollowerWatchAgent, *platform.Registry, codexFollowerSnapshot, chan struct{}) {
	t.Helper()
	h := NewHandler(nil, nil)
	if statePath != "" {
		h.SetCodexSessionFile(statePath)
	}
	workspace := t.TempDir()
	routeUserID := "feishu:bot:dm:chat:user"
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message",
	}
	bindingKey := codexBindingKey(routeUserID, "codex")
	store := h.ensureCodexSessions()
	store.setActiveWorkspace(bindingKey, workspace)
	store.setThread(bindingKey, workspace, "thread-local")
	store.mu.Lock()
	binding := store.bindings[bindingKey]
	binding.FollowRevision++
	binding.Follower = &codexFrontendFollower{
		WorkspaceRoot: workspace, ThreadID: "thread-local", ActorUserID: "user", AuthorizedIdentity: "user",
		DeliveryRoute: route, UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	store.bindings[bindingKey] = binding
	store.mu.Unlock()
	store.save()
	snapshot := store.followerSnapshots()[0]

	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	watchDone := make(chan struct{})
	base.watchDone = watchDone
	ag := &codexFollowerWatchAgent{fakeCodexLiveAgent: base, watchStarted: make(chan string, 4)}
	h.SetDefaultAgent("codex", ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	registry := newCodexFollowerTestRegistry(route, reply)
	return h, ag, registry, snapshot, watchDone
}

func newCodexFollowerTestRegistry(route platform.DeliveryRoute, reply platform.Replier) *platform.Registry {
	return platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &codexFollowerTestPlatform{name: route.Platform, account: route.AccountID, reply: reply},
		Access:   platform.NewAccessControl([]string{"user"}),
	}})
}
