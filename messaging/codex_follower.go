package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const defaultCodexFollowerReconcileInterval = 2 * time.Second
const codexFollowerRuntimeResultTimeout = 5 * time.Second

type codexFollowerService struct {
	registry       *platform.Registry
	interval       time.Duration
	wake           chan struct{}
	failures       map[string]codexFollowerFailureState
	resultFailures map[string]codexFollowerFailureState
}

type codexFollowerFailureState struct {
	summary string
	count   int
}

// StartCodexFollowerReconciler 恢复持久化的飞书同步端点，并持续观察本地端稍后启动的新 turn。
func (h *Handler) StartCodexFollowerReconciler(ctx context.Context, registry *platform.Registry) error {
	return h.startCodexFollowerReconciler(ctx, registry, defaultCodexFollowerReconcileInterval)
}

func (h *Handler) startCodexFollowerReconciler(ctx context.Context, registry *platform.Registry, interval time.Duration) error {
	if registry == nil {
		return fmt.Errorf("Codex follower registry is nil")
	}
	if interval <= 0 {
		interval = defaultCodexFollowerReconcileInterval
	}
	service := &codexFollowerService{
		registry:       registry,
		interval:       interval,
		wake:           make(chan struct{}, 1),
		failures:       make(map[string]codexFollowerFailureState),
		resultFailures: make(map[string]codexFollowerFailureState),
	}
	h.codexFollowerMu.Lock()
	if h.codexFollower != nil {
		h.codexFollowerMu.Unlock()
		return fmt.Errorf("Codex follower reconciler already started")
	}
	h.codexFollower = service
	h.codexFollowerMu.Unlock()
	go h.runCodexFollowerReconciler(normalizeContext(ctx), service)
	return nil
}

func (h *Handler) runCodexFollowerReconciler(ctx context.Context, service *codexFollowerService) {
	defer func() {
		h.codexFollowerMu.Lock()
		if h.codexFollower == service {
			h.codexFollower = nil
		}
		h.codexFollowerMu.Unlock()
	}()
	ticker := time.NewTicker(service.interval)
	defer ticker.Stop()
	for {
		h.reconcileCodexFollowersWithService(ctx, service)
		select {
		case <-ctx.Done():
			return
		case <-service.wake:
		case <-ticker.C:
		}
	}
}

func (h *Handler) reconcileCodexFollowers(ctx context.Context, registry *platform.Registry) {
	h.reconcileCodexFollowersWithService(ctx, &codexFollowerService{registry: registry})
}

func (h *Handler) reconcileCodexFollowersWithService(ctx context.Context, service *codexFollowerService) {
	registry := service.registry
	followers, releases := h.ensureCodexSessions().followerRecoverySnapshots()
	if outbox := h.currentTerminalOutbox(); outbox != nil {
		h.recoverReleasedCodexFollowerStreamsForTargets(outbox, committedCodexReleaseTargets(releases))
		outbox.reconcileCodexFollowerHolds(followers, releases)
		outbox.reconcileCodexFollowerTerminalDeliveryHolds(followers)
	}
	for _, snapshot := range followers {
		err := h.reconcileCodexFollower(ctx, registry, snapshot)
		var resultErr error
		if err == nil {
			if outbox := h.currentTerminalOutbox(); outbox != nil {
				outbox.releaseCodexFollowerTerminalDeliveries(snapshot)
			}
			resultErr = h.reconcileCodexFollowerRuntimeResult(ctx, registry, snapshot)
		}
		service.recordRuntimeResult(snapshot, resultErr, ctx.Err())
		service.recordReconcileResult(snapshot, err, ctx.Err())
	}
}

func (s *codexFollowerService) recordRuntimeResult(snapshot codexFollowerSnapshot, err error, contextErr error) {
	if s == nil || contextErr != nil {
		return
	}
	key := snapshot.BindingKey + "\x00" + snapshot.Target.ThreadID
	if err == nil {
		delete(s.resultFailures, key)
		return
	}
	if s.resultFailures == nil {
		s.resultFailures = make(map[string]codexFollowerFailureState)
	}
	state := s.resultFailures[key]
	if state.summary != err.Error() {
		state = codexFollowerFailureState{summary: err.Error()}
	}
	state.count++
	s.resultFailures[key] = state
	if state.count&(state.count-1) != 0 {
		return
	}
	log.Printf("[codex-follower] 运行通道已恢复但切换结果暂未更新 route=%q thread=%q revision=%d attempts=%d: %v",
		snapshot.BindingKey, snapshot.Target.ThreadID, snapshot.Revision, state.count, err)
}

func (s *codexFollowerService) recordReconcileResult(snapshot codexFollowerSnapshot, err error, contextErr error) {
	if s == nil || contextErr != nil {
		return
	}
	key := snapshot.BindingKey + "\x00" + snapshot.Target.ThreadID
	if err == nil {
		delete(s.failures, key)
		return
	}
	if s.failures == nil {
		s.failures = make(map[string]codexFollowerFailureState)
	}
	summary := err.Error()
	state := s.failures[key]
	if state.summary != summary {
		state = codexFollowerFailureState{summary: summary}
	}
	state.count++
	s.failures[key] = state
	// 首次立即记录；同一错误随后按 2 的幂次采样，避免两秒一次刷屏且仍可观察持续故障。
	if state.count&(state.count-1) != 0 {
		return
	}
	log.Printf("[codex-follower] 同步观察暂未恢复 route=%q thread=%q revision=%d attempts=%d: %v",
		snapshot.BindingKey, snapshot.Target.ThreadID, snapshot.Revision, state.count, err)
}

func (h *Handler) reconcileCodexFollowerRuntimeResult(
	ctx context.Context,
	registry *platform.Registry,
	expected codexFollowerSnapshot,
) error {
	if expected.Target.RuntimeRecoveryResult == nil {
		return nil
	}
	ready, err := codexFollowerRuntimeResultReady(*expected.Target.RuntimeRecoveryResult, time.Now())
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	unlock, err := h.lockCodexSessionBinding(ctx, expected.BindingKey, "follow-result")
	if err != nil {
		return err
	}
	defer unlock()
	current, ok := h.ensureCodexSessions().followerSnapshot(expected.BindingKey)
	if !ok || current.Revision != expected.Revision ||
		!sameCodexFrontendFollower(&current.Target, &expected.Target) {
		return nil
	}
	if current.AttachPhase != codexFollowerAttachReady {
		return nil
	}
	h.codexFollowerDeliveryMu.RLock()
	defer h.codexFollowerDeliveryMu.RUnlock()
	if !h.ensureCodexSessions().followerMatches(current) || !h.codexFollowerAuthorized(registry, current) {
		return nil
	}
	reply, ok := registry.ReplierForRoute(current.Target.DeliveryRoute)
	if !ok || reply == nil {
		return fmt.Errorf("飞书投递路由暂不可用")
	}
	durable, ok := optionalDurableCommandResultReplier(reply)
	if !ok {
		return platform.ErrUnsupported
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, codexFollowerRuntimeResultTimeout)
	defer cancel()
	content := h.renderCodexFollowerRuntimeRecovered(current)
	if err := durable.DeliverCommandResult(deliveryCtx, *current.Target.RuntimeRecoveryResult, content); err != nil {
		return err
	}
	return h.ensureCodexSessions().clearFollowerRuntimeRecovery(current)
}

func codexFollowerRuntimeResultReady(reference platform.DurableCommandResultReference, now time.Time) (bool, error) {
	readyAfter := strings.TrimSpace(reference.ReadyAfter)
	if readyAfter == "" {
		return true, nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, readyAfter)
	if err != nil {
		return false, fmt.Errorf("Codex 运行通道恢复卡引用时间无效: %w", err)
	}
	return !now.Before(deadline), nil
}

func (h *Handler) renderCodexFollowerRuntimeRecovered(snapshot codexFollowerSnapshot) string {
	return wechatCommandText(
		"已切换并绑定。",
		"工作空间: "+shortCodexWorkspaceName(snapshot.Target.WorkspaceRoot),
		renderCompactSessionModelStatus(h.codexSessionModelStatus(snapshot.Target.ThreadID)),
		"运行通道: 已恢复",
	)
}

func (h *Handler) reconcileCodexFollower(ctx context.Context, registry *platform.Registry, snapshot codexFollowerSnapshot) error {
	if !h.ensureCodexSessions().followerMatches(snapshot) {
		return nil
	}
	if !h.codexFollowerAuthorized(registry, snapshot) {
		h.detachUnauthorizedCodexFollowers(registry, snapshot.Target.DeliveryRoute.Platform,
			snapshot.Target.DeliveryRoute.AccountID)
		return nil
	}
	reply, ok := registry.ReplierForRoute(snapshot.Target.DeliveryRoute)
	if !ok || reply == nil {
		return fmt.Errorf("飞书投递路由暂不可用")
	}
	ag, err := h.EnsureAgentStarted(ctx, snapshot.AgentName)
	if err != nil {
		return err
	}
	opts := externalCodexTaskOptions{
		ctx:            ctx,
		actorUserID:    snapshot.Target.ActorUserID,
		routeUserID:    snapshot.RouteUserID,
		agentName:      snapshot.AgentName,
		agent:          ag,
		conversationID: snapshot.ConversationID,
		bindingKey:     snapshot.BindingKey,
		threadID:       snapshot.Target.ThreadID,
		workspaceRoot:  snapshot.Target.WorkspaceRoot,
		platform:       snapshot.Target.DeliveryRoute.Platform,
		accountID:      snapshot.Target.DeliveryRoute.AccountID,
		progressCfg: h.resolveProgressConfigForAccount(
			snapshot.Target.DeliveryRoute.Platform,
			snapshot.Target.DeliveryRoute.AccountID,
			snapshot.AgentName,
		),
		reply:                        reply,
		runtimeInactiveAuthoritative: true,
	}
	unlock, err := h.lockCodexSessionBinding(ctx, snapshot.BindingKey, "follow")
	if err != nil {
		return err
	}
	bindingLocked := true
	defer func() {
		if bindingLocked {
			unlock()
		}
	}()
	if !h.ensureCodexSessions().followerMatches(snapshot) {
		return nil
	}
	liveAgent, ok := ag.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return errCodexSessionAcquireUnsupported
	}
	unlockThread, err := h.lockCodexSessionThread(ctx, snapshot.Target.ThreadID, "follow")
	if err != nil {
		return err
	}
	threadLocked := true
	defer func() {
		if threadLocked {
			unlockThread()
		}
	}()
	h.codexFollowerDeliveryMu.RLock()
	deliveryLocked := true
	defer func() {
		if deliveryLocked {
			h.codexFollowerDeliveryMu.RUnlock()
		}
	}()
	if !h.ensureCodexSessions().followerMatches(snapshot) {
		return nil
	}
	if !h.codexFollowerAuthorized(registry, snapshot) {
		h.codexFollowerDeliveryMu.RUnlock()
		deliveryLocked = false
		unlockThread()
		threadLocked = false
		unlock()
		bindingLocked = false
		h.detachUnauthorizedCodexFollowers(registry, snapshot.Target.DeliveryRoute.Platform,
			snapshot.Target.DeliveryRoute.AccountID)
		return nil
	}
	route := codexConversationRoute{
		bindingKey: snapshot.BindingKey, conversationID: snapshot.ConversationID,
		workspaceRoot: snapshot.Target.WorkspaceRoot, threadID: snapshot.Target.ThreadID,
	}
	request := h.buildCodexRuntimeRequestForTurn(route, snapshot.Target.ThreadID)
	runtimeBinding, err := ensureCodexFollowerRuntime(ctx, liveAgent, request)
	if err != nil {
		return err
	}
	opts.runtimeGeneration = runtimeBinding.RuntimeGeneration
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil {
		return err
	}
	if !h.ensureCodexSessions().followerMatches(snapshot) {
		return nil
	}
	if prepared.active {
		opts.terminalDeliveryKey = codexFollowerTerminalOutboxID(snapshot, prepared.state.ActiveTurnID)
		reconciledBinding, reconcileErr := liveAgent.ReconcileCodexObservedTurn(ctx, request, prepared.state.CodexThreadState)
		if reconcileErr != nil {
			return reconcileErr
		}
		opts.runtimeGeneration = reconciledBinding.RuntimeGeneration
	}
	unlockThread()
	threadLocked = false
	attachTurnID := strings.TrimSpace(prepared.state.LastTurnID)
	if prepared.active {
		attachTurnID = strings.TrimSpace(prepared.state.ActiveTurnID)
	}
	if owned, readyErr := h.commitOwnedCodexFollowerAttachReady(
		snapshot, prepared.state, attachTurnID, opts.runtimeGeneration,
	); owned {
		return readyErr
	}
	attachMatchesRuntime := snapshot.AttachTurnID == attachTurnID &&
		snapshot.RuntimeGeneration == opts.runtimeGeneration
	needsPreparing := !attachMatchesRuntime ||
		(prepared.active && snapshot.AttachPhase != codexFollowerAttachPreparing) ||
		(!prepared.active && snapshot.AttachPhase == "")
	if needsPreparing {
		snapshot, err = h.ensureCodexSessions().commitFollowerAttachRuntime(
			snapshot, attachTurnID, opts.runtimeGeneration,
		)
		if err != nil {
			return err
		}
	}
	if prepared.active {
		opts.followerAttach = &snapshot
	} else if snapshot.AttachPhase != codexFollowerAttachReady {
		if err := h.ensureCodexSessions().commitFollowerAttachReady(
			snapshot, attachTurnID, opts.runtimeGeneration,
		); err != nil {
			return err
		}
		if current, ok := h.ensureCodexSessions().followerSnapshot(snapshot.BindingKey); ok {
			snapshot = current
		}
	}
	recoveredTerminal, recoveryErr := h.reconcileCodexFollowerRecoveries(snapshot, prepared.state, reply)
	if recoveryErr != nil {
		log.Printf("[codex-follower] 恢复旧进度卡失败 route=%q thread=%q: %v",
			snapshot.BindingKey, snapshot.Target.ThreadID, recoveryErr)
	}
	if recoveredTerminal {
		if recoveryErr != nil {
			return recoveryErr
		}
		return h.ensureCodexSessions().commitFollowerTurnClaim(snapshot, prepared.state.LastTurnID)
	}
	if !prepared.active {
		return h.reconcileInactiveCodexFollower(snapshot, prepared.state)
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		return err
	}
	reservation.task.setTerminalDeliveryKey(opts.terminalDeliveryKey)
	reservation.task.setTerminalDeliveryGuard(terminalDeliveryGuardFromFollower(snapshot))
	if err := h.ensureCodexSessions().commitFollowerTurnPending(snapshot, prepared.state.ActiveTurnID); err != nil {
		h.cancelExternalCodexTaskReservation(reservation)
		return err
	}
	if !h.activateExternalCodexTaskReservation(reservation) {
		h.cancelExternalCodexTaskReservation(reservation)
		// Pending 游标必须保留：它明确表示尚无 durable observer/outbox 取得终态责任。
		// 后续调和会重试观察；若 turn 已结束，inactive 分支会补投确定性终态。
		return errExternalCodexTaskReservationConflict
	}
	return h.waitExternalCodexTaskReservationReady(ctx, reservation)
}

func (h *Handler) codexFollowerAuthorized(registry *platform.Registry, snapshot codexFollowerSnapshot) bool {
	return registry.AllowsStoredIdentity(
		snapshot.Target.DeliveryRoute.Platform,
		snapshot.Target.DeliveryRoute.AccountID,
		[]string{snapshot.Target.AuthorizedIdentity},
	)
}

type codexHostTopologyReconciler interface {
	ReconcileCodexHostTopology(context.Context) error
}

func ensureCodexFollowerRuntime(ctx context.Context, liveAgent agent.CodexLiveRuntimeAgent, request agent.CodexRuntimeRequest) (agent.CodexThreadBinding, error) {
	if reconciler, ok := liveAgent.(codexHostTopologyReconciler); ok {
		if err := reconciler.ReconcileCodexHostTopology(ctx); err != nil {
			return agent.CodexThreadBinding{}, err
		}
	}
	binding, err := liveAgent.CurrentCodexRuntime(request)
	if err == nil && codexRuntimeReadyForRemoteTurn(binding.Runtime) {
		return binding, nil
	}
	binding, err = liveAgent.HandoffCodexRuntime(ctx, request)
	if err != nil {
		return agent.CodexThreadBinding{}, err
	}
	binding, err = liveAgent.CurrentCodexRuntime(request)
	if err != nil {
		return agent.CodexThreadBinding{}, err
	}
	if !codexRuntimeReadyForRemoteTurn(binding.Runtime) {
		return binding, agent.ErrCodexRuntimeUnavailable
	}
	return binding, nil
}

type codexFollowerActiveTaskReadiness struct {
	matched       bool
	inProcess     bool
	progressErr   error
	ready         agent.CodexThreadObserverReady
	readySeen     bool
	readyErr      error
	readyComplete bool
}

func (h *Handler) activeTaskCodexFollowerReadiness(
	snapshot codexFollowerSnapshot,
	state externalCodexTaskState,
) codexFollowerActiveTaskReadiness {
	targetTurnID := strings.TrimSpace(state.ActiveTurnID)
	if targetTurnID == "" {
		targetTurnID = strings.TrimSpace(state.LastTurnID)
	}
	h.tasks.mu.Lock()
	task := h.tasks.active[snapshot.ConversationID]
	h.tasks.mu.Unlock()
	if task == nil {
		return codexFollowerActiveTaskReadiness{}
	}
	task.mu.Lock()
	if task.codexThreadID != strings.TrimSpace(snapshot.Target.ThreadID) ||
		task.codexTurnID != targetTurnID {
		task.mu.Unlock()
		return codexFollowerActiveTaskReadiness{}
	}
	readiness := codexFollowerActiveTaskReadiness{
		matched: true, inProcess: task.inProcessCodexLifecycle,
	}
	control := task.externalReservation
	task.mu.Unlock()
	if readiness.inProcess {
		readiness.progressErr = task.nativeProgressCardReadyError()
	}
	if control != nil {
		readiness.ready, readiness.readySeen, readiness.readyErr, readiness.readyComplete =
			control.observerReadyResult()
	}
	return readiness
}

func (h *Handler) commitOwnedCodexFollowerAttachReady(
	snapshot codexFollowerSnapshot,
	state externalCodexTaskState,
	turnID string,
	runtimeGeneration uint64,
) (bool, error) {
	readiness := h.activeTaskCodexFollowerReadiness(snapshot, state)
	if !readiness.matched {
		return false, nil
	}
	if snapshot.AttachPhase == codexFollowerAttachReady &&
		snapshot.AttachTurnID == turnID && snapshot.RuntimeGeneration == runtimeGeneration {
		return true, nil
	}
	if snapshot.AttachPhase != codexFollowerAttachPreparing ||
		snapshot.AttachTurnID != turnID || snapshot.RuntimeGeneration != runtimeGeneration {
		prepared, err := h.ensureCodexSessions().commitFollowerAttachRuntime(
			snapshot, turnID, runtimeGeneration,
		)
		if err != nil {
			return true, err
		}
		snapshot = prepared
	}
	if readiness.inProcess {
		if readiness.progressErr != nil {
			return true, readiness.progressErr
		}
		return true, h.ensureCodexSessions().commitFollowerAttachReady(
			snapshot, turnID, runtimeGeneration,
		)
	}
	if !readiness.readyComplete {
		return true, fmt.Errorf("现有 Codex observer 尚未完成就绪校验")
	}
	if readiness.readyErr != nil {
		return true, readiness.readyErr
	}
	if !readiness.readySeen {
		return true, fmt.Errorf("现有 Codex observer 缺少就绪证据")
	}
	return true, h.commitCodexObserverReadyForAttach(
		&snapshot, snapshot.Target.ThreadID, turnID, readiness.ready,
	)
}

func (h *Handler) reconcileInactiveCodexFollower(snapshot codexFollowerSnapshot, state externalCodexTaskState) error {
	turnID := strings.TrimSpace(state.LastTurnID)
	if !snapshot.FollowTurnInitialized {
		return h.ensureCodexSessions().commitFollowerTurnClaim(snapshot, turnID)
	}
	if turnID == "" || turnID == strings.TrimSpace(snapshot.FollowTurnID) && !snapshot.FollowTurnPending {
		return nil
	}
	draft, terminal := codexFollowerTerminalDraft(snapshot, state)
	if !terminal {
		return nil
	}
	outbox := h.currentTerminalOutbox()
	if outbox == nil {
		return ErrTerminalOutboxUnavailable
	}
	if _, err := outbox.enqueue(draft); err != nil {
		return err
	}
	outbox.signal()
	return h.ensureCodexSessions().commitFollowerTurnClaim(snapshot, turnID)
}

func codexFollowerTerminalDraft(snapshot codexFollowerSnapshot, state externalCodexTaskState) (terminalOutboxDraft, bool) {
	status := strings.ToLower(strings.TrimSpace(state.LastTurnStatus))
	text := ""
	failed, stopped := false, false
	switch status {
	case "completed":
		text = firstNonBlank(state.LastAgentMessageText, "Codex App 本地任务已完成，但没有返回文本。")
	case "failed", "error":
		failed = true
		text = renderFinalFailure("", fmt.Errorf("Codex 本地任务执行失败"))
	case "interrupted", "cancelled", "canceled":
		stopped = true
		text = renderFinalStopped("")
	default:
		return terminalOutboxDraft{}, false
	}
	return terminalOutboxDraft{
		ID:                 codexFollowerTerminalOutboxID(snapshot, state.LastTurnID),
		Route:              snapshot.Target.DeliveryRoute,
		AgentName:          snapshot.AgentName,
		AuthorizedIdentity: snapshot.Target.AuthorizedIdentity,
		FollowerBindingKey: snapshot.BindingKey, FollowerRevision: snapshot.Revision,
		FollowerThreadID: snapshot.Target.ThreadID,
		Failed:           failed,
		Stopped:          stopped,
		ResultTitle:      progressResultTitleForAgentWorkspace(snapshot.AgentName, snapshot.Target.WorkspaceRoot, 60),
		RichResult:       true,
		Text:             text,
		IdempotencyKey:   codexFollowerTerminalOutboxID(snapshot, state.LastTurnID),
	}, true
}

func codexFollowerTerminalOutboxID(snapshot codexFollowerSnapshot, turnID string) string {
	route := snapshot.Target.DeliveryRoute
	identity := strings.Join([]string{
		"weclaw.codex-follower-terminal.v1",
		string(route.Platform), strings.TrimSpace(route.AccountID), strings.TrimSpace(route.ChatID),
		strings.TrimSpace(snapshot.BindingKey), strings.TrimSpace(snapshot.Target.AuthorizedIdentity),
		strings.TrimSpace(snapshot.AgentName),
		strings.TrimSpace(snapshot.Target.ThreadID), strings.TrimSpace(turnID),
	}, "\x00")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
}

func (h *Handler) reconcileCodexFollowerRecoveries(snapshot codexFollowerSnapshot, state externalCodexTaskState, reply platform.Replier) (bool, error) {
	outbox := h.currentTerminalOutbox()
	if outbox == nil {
		return false, nil
	}
	entries := outbox.heldCodexFollowerRecoveries(snapshot)
	var joined error
	terminalRecovered := false
	for _, entry := range entries {
		if entry.Stream == nil || entry.Trace == nil {
			continue
		}
		journalReservationID := strings.TrimSpace(snapshot.RecoveryReservationID)
		journalEntry := journalReservationID != "" && entry.ID == journalReservationID
		if journalEntry {
			recoveredTurnID := strings.TrimSpace(state.LastTurnID)
			if state.Active {
				recoveredTurnID = strings.TrimSpace(state.ActiveTurnID)
			}
			if recoveredTurnID != "" {
				recoveredTrace := *entry.Trace
				recoveredTrace.ThreadID = strings.TrimSpace(snapshot.Target.ThreadID)
				recoveredTrace.TurnID = recoveredTurnID
				if recoveredTrace.ThreadID != entry.Trace.ThreadID || recoveredTrace.TurnID != entry.Trace.TurnID {
					if err := outbox.refreshStreamReservationTrace(entry.ID, recoveredTrace); err != nil {
						joined = errors.Join(joined, err)
						continue
					}
					entry.Trace = &recoveredTrace
				}
			}
		}
		entryTurnID := strings.TrimSpace(entry.Trace.TurnID)
		if !state.Active {
			if entryTurnID != "" && entryTurnID == strings.TrimSpace(state.LastTurnID) {
				if err := h.finalizeHeldCodexFollowerRecovery(outbox, entry, snapshot, state); err != nil {
					joined = errors.Join(joined, err)
				} else {
					terminalRecovered = true
				}
			} else {
				outbox.releaseHeldCodexFollowerRecovery(entry.ID)
			}
			continue
		}
		activeTurnID := strings.TrimSpace(state.ActiveTurnID)
		if entryTurnID == "" || activeTurnID == "" || entryTurnID != activeTurnID {
			// durable binding 指向 thread，而恢复条目属于具体 turn。旧 turn 的终态必须
			// 重新交给 outbox 投递，不能被新 active turn 当成活动进度卡清空。
			outbox.releaseHeldCodexFollowerRecovery(entry.ID)
			continue
		}
		preparer, ok := optionalDurableStreamSupersedePreparer(reply)
		if !ok {
			joined = errors.Join(joined, platform.ErrUnsupported)
			continue
		}
		operationID := uuid.NewString()
		notice := "WeClaw 已恢复会话同步；后续进度将在新卡片继续展示。"
		checkpoint, err := preparer.PrepareSupersedeFromReference(*entry.Stream, notice, operationID)
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		if err := outbox.detachStreamReservation(entry.ID, pendingStreamSupersede{
			ID: operationID, Route: entry.Route, Checkpoint: checkpoint,
		}); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	if joined == nil && strings.TrimSpace(snapshot.RecoveryThreadID) != "" &&
		(strings.TrimSpace(state.ActiveTurnID) != "" || strings.TrimSpace(state.LastTurnID) != "") {
		h.ensureCodexSessions().clearPendingFirstTurn(
			snapshot.BindingKey,
			snapshot.Target.WorkspaceRoot,
			snapshot.Target.ThreadID,
		)
		h.ensureCodexSessions().clearFirstTurnRecoveryJournal(
			snapshot.BindingKey,
			snapshot.Target.WorkspaceRoot,
			snapshot.Target.ThreadID,
			snapshot.RecoveryThreadID,
			snapshot.RecoveryReservationID,
		)
	}
	return terminalRecovered, joined
}

func (h *Handler) finalizeHeldCodexFollowerRecovery(
	outbox *terminalOutbox,
	entry *terminalOutboxEntry,
	snapshot codexFollowerSnapshot,
	state externalCodexTaskState,
) error {
	status := strings.ToLower(strings.TrimSpace(state.LastTurnStatus))
	stopped := status == "interrupted" || status == "cancelled" || status == "canceled"
	failed := status == "failed" || status == "error"
	text := strings.TrimSpace(state.LastAgentMessageText)
	if stopped {
		text = renderFinalStopped("")
	} else if failed {
		text = renderFinalFailure("", fmt.Errorf("Codex 本地任务执行失败"))
	} else if text == "" {
		text = "Codex 本地任务已完成，但没有返回文本。"
	}
	trace := observability.TraceContext{}
	if entry.Trace != nil {
		trace = *entry.Trace
	}
	if err := outbox.stageReservationResult(entry.ID, terminalOutboxDraft{
		Route: entry.Route, AgentName: entry.AgentName, Failed: failed, Stopped: stopped,
		AuthorizedIdentity: snapshot.Target.AuthorizedIdentity,
		FollowerBindingKey: snapshot.BindingKey, FollowerRevision: snapshot.Revision,
		FollowerThreadID: snapshot.Target.ThreadID,
		ResultTitle:      entry.ResultTitle, RichResult: entry.RichResult, Text: text,
		IdempotencyKey: codexFollowerTerminalOutboxID(snapshot, state.LastTurnID), Trace: trace,
	}); err != nil {
		return err
	}
	outbox.releaseHeldCodexFollowerRecovery(entry.ID)
	return nil
}
