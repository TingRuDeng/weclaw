package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

var (
	errCodexSessionAcquireActiveOld   = errors.New("当前会话任务仍在执行")
	errCodexSessionAcquireUncertain   = errors.New("Codex 会话绑定结果未确认")
	errCodexSessionAcquireUnsupported = errors.New("当前 Codex Agent 不支持共享 app-server 会话绑定")
	errCodexFollowerAccessChanged     = errors.New("飞书授权状态已变化，请重新发送命令")
)

// codexSessionAcquireRequest describes one frontend binding operation. The
// route identifies a client view; it is not a global writer owner.
type codexSessionAcquireRequest struct {
	ctx                context.Context
	actorUserID        string
	authorizedIdentity string
	routeUserID        string
	agentName          string
	agent              agent.Agent
	route              codexConversationRoute
	platform           platform.PlatformName
	accountID          string
	reply              platform.Replier
	taskContext        context.Context
	// pendingFirstTurn marks a thread created by thread/start that has not yet
	// accepted its first user turn.
	pendingFirstTurn bool
}

type codexSessionAcquireResult struct {
	route                   codexConversationRoute
	resolution              codexRuntimeResolution
	externalState           externalCodexTaskState
	externalActive          bool
	externalProgressCard    bool
	agentSessionErr         error
	runtimeErr              error
	selectionChanged        bool
	progressReanchored      bool
	progressReanchorErr     error
	handoffReleaseAttempted bool
	handoffReleaseRetained  bool
	handoffReleaseThreadID  string
	handoffReleaseErr       error
}

// acquireCodexSessionWithBindingLocked atomically commits one frontend's
// workspace/thread binding, then asks the shared app-server client to bind its
// conversation mapping to that thread. Other durable frontend bindings are
// never released; an idle Host may be recycled to return the old thread lock.
func (h *Handler) acquireCodexSessionWithBindingLocked(req codexSessionAcquireRequest) (codexSessionAcquireResult, error) {
	liveAgent, ok := req.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return codexSessionAcquireResult{}, errCodexSessionAcquireUnsupported
	}
	if err := h.guardCodexThreadSwitch(req.route, req.route.threadID); err != nil {
		return codexSessionAcquireResult{}, err
	}

	store := h.ensureCodexSessions()
	initial := store.remoteSelectionSnapshot(req.route.bindingKey, req.route.threadID)
	unlock, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
		ctx: req.ctx, command: "bind", threadIDs: codexRemoteSelectionThreadIDs(initial),
	})
	if err != nil {
		return codexSessionAcquireResult{}, err
	}
	defer unlock()

	locked := store.remoteSelectionSnapshot(req.route.bindingKey, req.route.threadID)
	if !sameCodexRemoteSelectionSnapshot(initial, locked) {
		return codexSessionAcquireResult{}, errCodexRemoteSelectionChanged
	}
	h.bindConversationCwd(req.agent, req.route.conversationID, req.route.workspaceRoot)
	providerRequest, providerRollout, err := h.buildCodexRuntimeRequest(req.route, req.route.threadID)
	if err != nil {
		return codexSessionAcquireResult{}, err
	}
	providerPreparation := agent.CodexProviderPreparation{}
	if providerAgent, supported := req.agent.(agent.CodexProviderRuntimeAgent); supported {
		providerPreparation, err = providerAgent.PrepareCodexThread(req.ctx, providerRequest)
		if err != nil {
			return codexSessionAcquireResult{}, err
		}
	}
	preBindRuntime, preBindRuntimeErr := liveAgent.CurrentCodexRuntime(providerRequest)
	unlockFollowerDelivery := func() {}
	if req.platform == platform.PlatformFeishu {
		h.codexFollowerDeliveryMu.RLock()
		unlockFollowerDelivery = h.codexFollowerDeliveryMu.RUnlock
		if !h.codexFollowerIdentityAuthorized(req.platform, req.accountID, req.authorizedIdentity) {
			unlockFollowerDelivery()
			return codexSessionAcquireResult{}, errCodexFollowerAccessChanged
		}
	}
	defer unlockFollowerDelivery()
	follower, setFollower := codexFollowerFromAcquire(req)
	followerBaseline := codexFollowerBaselineFromSources(providerRollout, preBindRuntime, preBindRuntimeErr)
	committed, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey:              req.route.bindingKey,
		WorkspaceRoot:           req.route.workspaceRoot,
		TargetThreadID:          req.route.threadID,
		ConversationID:          req.route.conversationID,
		PendingFirstTurn:        req.pendingFirstTurn,
		SetFollower:             setFollower,
		Follower:                follower,
		FollowerTurnID:          followerBaseline.turnID,
		FollowerTurnInitialized: setFollower && followerBaseline.initialized,
		FollowerTurnPending:     setFollower && followerBaseline.pending,
		Expected:                locked,
	})
	if err != nil {
		return codexSessionAcquireResult{}, err
	}

	result := h.finishCodexFrontendBinding(req)
	if result.agentSessionErr != nil {
		rollbackErr := store.rollbackRemoteSelection(committed)
		if rollbackErr != nil {
			return codexSessionAcquireResult{}, errors.Join(
				errCodexSessionAcquireUncertain, result.agentSessionErr, rollbackErr,
			)
		}
		return codexSessionAcquireResult{}, result.agentSessionErr
	}
	result = h.recoverPreviousCodexThreadHandoff(result, req, locked)
	storeSelectionChanged := !codexRemoteSelectionMatchesRoute(locked, req.route)
	result.selectionChanged = h.codexTaskCardSelectionChanged(
		req.route.bindingKey, req.route.conversationID, storeSelectionChanged,
	)
	if providerPreparation.Deferred && !providerPreparation.TargetActive && !providerRequest.Checkpoint.Active {
		result.resolution = codexRuntimeResolution{
			Request: providerRequest, Binding: unknownCodexRuntimeBinding(providerRequest),
			Rollout: providerRollout, Live: true, ProbeErr: agent.ErrCodexWriterBusy,
		}
		result.runtimeErr = agent.ErrCodexWriterBusy
		return h.recordCodexRuntimeRecoveryResult(req, result), nil
	}

	result.resolution, result.runtimeErr = h.bindCodexSharedRuntime(req, liveAgent)
	if result.runtimeErr != nil {
		return h.recordCodexRuntimeRecoveryResult(req, result), nil
	}
	result, err = h.attachCodexAcquireObserver(result, req, liveAgent)
	if result.runtimeErr != nil {
		result = h.recordCodexRuntimeRecoveryResult(req, result)
	}
	if err == nil && result.runtimeErr == nil &&
		(result.progressReanchorErr == nil || result.progressReanchored) {
		h.commitCodexTaskCardFocus(req.route.bindingKey, req.route.conversationID)
	}
	return result, err
}

func (h *Handler) recordCodexRuntimeRecoveryResult(
	req codexSessionAcquireRequest,
	result codexSessionAcquireResult,
) codexSessionAcquireResult {
	if result.runtimeErr == nil || req.platform != platform.PlatformFeishu || req.reply == nil {
		return result
	}
	reporter, ok := optionalDurableCommandResultReferenceReporter(req.reply)
	if !ok {
		return result
	}
	reference, err := reporter.DurableCommandResultReference()
	if err != nil || !reference.Valid() {
		if err != nil && !errors.Is(err, platform.ErrUnsupported) {
			log.Printf("[codex-session-bind] 无法保存运行通道恢复卡引用 thread=%q: %v", result.route.threadID, err)
		}
		return result
	}
	store := h.ensureCodexSessions()
	snapshot, ok := store.followerSnapshot(result.route.bindingKey)
	if !ok || snapshot.Target.ThreadID != result.route.threadID {
		return result
	}
	if err := store.commitFollowerRuntimeRecovery(snapshot, reference); err != nil {
		log.Printf("[codex-session-bind] 保存运行通道恢复卡引用失败 thread=%q: %v", result.route.threadID, err)
		return result
	}
	h.wakeCodexFollowerReconciler()
	return result
}

type codexFollowerBaseline struct {
	turnID      string
	initialized bool
	pending     bool
}

func codexFollowerBaselineFromSources(
	rollout codexRolloutTaskState,
	binding agent.CodexThreadBinding,
	stateErr error,
) codexFollowerBaseline {
	if stateErr == nil && codexRuntimeReadyForRemoteTurn(binding.Runtime) {
		state := binding.State
		if state.Active {
			if turnID := firstNonBlank(state.ActiveTurnID, state.LastTurnID); turnID != "" {
				return codexFollowerBaseline{turnID: turnID, initialized: true, pending: true}
			}
		} else if turnID := strings.TrimSpace(state.LastTurnID); turnID != "" {
			return codexFollowerBaseline{turnID: turnID, initialized: true}
		}
	}
	if turnID := strings.TrimSpace(rollout.TurnID); turnID != "" {
		return codexFollowerBaseline{turnID: turnID, initialized: true, pending: rollout.Active}
	}
	// 绑定提交本身就是 follower 的观察起点。即使 Desktop handler 暂不可用，
	// 也要持久化一个空基线；否则在首次成功调和前快速完成的第一轮会被误当成历史。
	// 若本地 rollout 或权威 runtime 能证明既有 turn，上面的分支已经记录其 ID。
	return codexFollowerBaseline{initialized: true}
}

func (h *Handler) recoverPreviousCodexThreadHandoff(
	result codexSessionAcquireResult,
	req codexSessionAcquireRequest,
	previous codexRemoteSelectionSnapshot,
) codexSessionAcquireResult {
	previousThreadID := codexRemoteSelectionActiveThreadID(previous)
	if req.pendingFirstTurn || previousThreadID == "" || previousThreadID == strings.TrimSpace(req.route.threadID) {
		return result
	}
	result.handoffReleaseThreadID = previousThreadID
	if h.ensureCodexSessions().activeFrontendUsesThread(previousThreadID) {
		result.handoffReleaseRetained = true
		return result
	}
	handoffAgent, ok := req.agent.(agent.CodexThreadHandoffAgent)
	if !ok {
		return result
	}
	attempted, err := handoffAgent.RecoverCodexThreadHandoff(req.ctx, previousThreadID)
	result.handoffReleaseAttempted = attempted
	result.handoffReleaseErr = err
	return result
}

// finishCodexFrontendBinding switches only this message route's workspace and
// selected Agent. It does not change any app-server writer authority.
func (h *Handler) finishCodexFrontendBinding(request codexSessionAcquireRequest) codexSessionAcquireResult {
	agentSessionErr := h.ensureAgentSessions().Set(request.routeUserID, request.agentName)
	if agentSessionErr != nil {
		return codexSessionAcquireResult{route: request.route, agentSessionErr: agentSessionErr}
	}
	h.switchCodexWorkspaceForRoute(
		firstNonBlank(request.actorUserID, request.routeUserID),
		request.routeUserID, request.agentName,
		request.route.workspaceRoot, request.agent,
	)
	return codexSessionAcquireResult{route: request.route}
}

func externalCodexTaskOptionsFromAcquire(req codexSessionAcquireRequest) externalCodexTaskOptions {
	taskContext := req.taskContext
	if taskContext == nil {
		taskContext = normalizeContext(req.ctx)
	}
	return externalCodexTaskOptions{
		ctx: taskContext, actorUserID: req.actorUserID,
		routeUserID: req.routeUserID, agentName: req.agentName,
		agent: req.agent, conversationID: req.route.conversationID,
		bindingKey: req.route.bindingKey,
		threadID:   req.route.threadID, workspaceRoot: req.route.workspaceRoot, platform: req.platform,
		accountID: req.accountID, reply: req.reply,
	}
}

// attachCodexAcquireObserver mirrors a turn already active in the shared host.
// Failure affects progress mirroring only; the frontend binding remains valid.
func (h *Handler) attachCodexAcquireObserver(result codexSessionAcquireResult, req codexSessionAcquireRequest, liveAgent agent.CodexLiveRuntimeAgent) (codexSessionAcquireResult, error) {
	opts := externalCodexTaskOptionsFromAcquire(req)
	opts.runtimeGeneration = result.resolution.Binding.RuntimeGeneration
	// 只有绑定事务最初确实看见过 active turn，后续的 inactive 快照才是
	// 同一事务内的权威终态确认。否则仍要允许 rollout 作为跨进程活动证据，
	// 避免 app-server 暂时空闲的快照覆盖本地仍在运行的任务。
	opts.runtimeInactiveAuthoritative = result.resolution.Binding.State.Active
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil {
		return h.failCodexAcquireRuntime(result, liveAgent, err), nil
	}
	if prepared.state.Controllable && (prepared.active || result.resolution.Binding.State.Active) {
		controlCtx, cancel := h.codexThreadControlContext(req.ctx)
		binding, reconcileErr := liveAgent.ReconcileCodexObservedTurn(
			controlCtx, result.resolution.Request, prepared.state.CodexThreadState,
		)
		cancel()
		if reconcileErr != nil {
			return h.failCodexAcquireRuntime(result, liveAgent, reconcileErr), nil
		}
		result.resolution.Binding = binding
		opts.runtimeGeneration = binding.RuntimeGeneration
	}
	if result.resolution.Binding.State.Active &&
		!prepared.confirmedInactive && (!prepared.active || !prepared.state.Controllable) {
		err = fmt.Errorf("共享 app-server 的活动任务暂不能建立观察流")
		return h.failCodexAcquireRuntime(result, liveAgent, err), nil
	}
	if prepared.active {
		if snapshot, ok := h.ensureCodexSessions().followerSnapshot(req.route.bindingKey); ok {
			opts.terminalDeliveryKey = codexFollowerTerminalOutboxID(snapshot, prepared.state.ActiveTurnID)
		}
	}
	if snapshot, ok := h.ensureCodexSessions().followerSnapshot(req.route.bindingKey); ok {
		turnID := strings.TrimSpace(prepared.state.LastTurnID)
		if prepared.active {
			turnID = strings.TrimSpace(prepared.state.ActiveTurnID)
		}
		preparedAttach, prepareErr := h.ensureCodexSessions().commitFollowerAttachRuntime(
			snapshot, turnID, opts.runtimeGeneration,
		)
		if prepareErr != nil {
			return h.failCodexAcquireRuntime(result, liveAgent, prepareErr), nil
		}
		opts.followerAttach = &preparedAttach
		if !prepared.active {
			if readyErr := h.ensureCodexSessions().commitFollowerAttachReady(
				preparedAttach, turnID, opts.runtimeGeneration,
			); readyErr != nil {
				return h.failCodexAcquireRuntime(result, liveAgent, readyErr), nil
			}
		}
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		return h.failCodexAcquireRuntime(result, liveAgent, err), nil
	}
	if prepared.active {
		if snapshot, ok := h.ensureCodexSessions().followerSnapshot(req.route.bindingKey); ok {
			reservation.task.setTerminalDeliveryKey(opts.terminalDeliveryKey)
			reservation.task.setTerminalDeliveryGuard(terminalDeliveryGuardFromFollower(snapshot))
			if err := h.ensureCodexSessions().commitFollowerTurnPending(snapshot, prepared.state.ActiveTurnID); err != nil {
				h.cancelExternalCodexTaskReservation(reservation)
				return h.failCodexAcquireRuntime(result, liveAgent, err), nil
			}
		}
	}
	observerReady := h.activateExternalCodexTaskReservation(reservation)
	if prepared.active && !observerReady {
		h.cancelExternalCodexTaskReservation(reservation)
		return h.failCodexAcquireRuntime(result, liveAgent, errExternalCodexTaskReservationConflict), nil
	}
	if prepared.active && observerReady {
		if readyErr := h.waitExternalCodexTaskReservationReady(req.ctx, reservation); readyErr != nil {
			result.externalState = prepared.state
			return h.failCodexAcquireRuntime(result, liveAgent, readyErr), nil
		}
		if reservation.reused && opts.followerAttach != nil {
			if reservation.control == nil {
				if readyErr := reservation.task.nativeProgressCardReadyError(); readyErr != nil {
					result.externalState = prepared.state
					return h.failCodexAcquireRuntime(result, liveAgent, readyErr), nil
				}
				readyErr := h.commitCodexObserverReadyForAttach(
					opts.followerAttach, opts.threadID, prepared.state.ActiveTurnID,
					agent.CodexThreadObserverReady{
						ThreadID: opts.threadID, TurnID: prepared.state.ActiveTurnID,
						RuntimeGeneration: opts.runtimeGeneration,
					},
				)
				if readyErr != nil {
					result.externalState = prepared.state
					return h.failCodexAcquireRuntime(result, liveAgent, readyErr), nil
				}
			} else {
				ready, seen, readyErr, complete := reservation.control.observerReadyResult()
				if !complete || readyErr != nil || !seen {
					if readyErr == nil {
						readyErr = fmt.Errorf("已复用的 Codex observer 缺少就绪证据")
					}
					result.externalState = prepared.state
					return h.failCodexAcquireRuntime(result, liveAgent, readyErr), nil
				}
				if readyErr = h.commitCodexObserverReadyForAttach(
					opts.followerAttach, opts.threadID, prepared.state.ActiveTurnID, ready,
				); readyErr != nil {
					result.externalState = prepared.state
					return h.failCodexAcquireRuntime(result, liveAgent, readyErr), nil
				}
			}
		}
	}
	result.externalState = prepared.state
	result.externalActive = prepared.active && observerReady
	result.externalProgressCard = result.externalActive &&
		h.externalTaskReservationUsesProgressCard(reservation, opts)
	if result.selectionChanged && reservation.reused && observerReady {
		result.progressReanchored, result.progressReanchorErr = h.reanchorActiveCodexTask(
			req.ctx, reservation.task, req.reply,
		)
	}
	return result, nil
}

func codexRemoteSelectionMatchesRoute(snapshot codexRemoteSelectionSnapshot, route codexConversationRoute) bool {
	workspaceRoot := normalizeCodexWorkspaceRoot(route.workspaceRoot)
	if normalizeCodexWorkspaceRoot(snapshot.Binding.ActiveWorkspace) != workspaceRoot {
		return false
	}
	session := snapshot.Binding.Workspaces[workspaceRoot]
	return !session.PendingNewThread && strings.TrimSpace(session.ThreadID) == strings.TrimSpace(route.threadID)
}

// codexTaskCardSelectionChanged compares the target with the last session that
// completed frontend acquisition. Browsing a workspace may update the persisted
// selection before the user chooses a session, so that store alone cannot tell
// whether a running task card needs to be moved back to the message bottom.
func (h *Handler) codexTaskCardSelectionChanged(bindingKey string, conversationID string, fallback bool) bool {
	bindingKey = strings.TrimSpace(bindingKey)
	conversationID = strings.TrimSpace(conversationID)
	if bindingKey == "" || conversationID == "" {
		return fallback
	}
	h.codexTaskCardFocusMu.Lock()
	defer h.codexTaskCardFocusMu.Unlock()
	previous, tracked := h.codexTaskCardFocus[bindingKey]
	if !tracked {
		return fallback
	}
	return previous != conversationID
}

func (h *Handler) commitCodexTaskCardFocus(bindingKey string, conversationID string) {
	bindingKey = strings.TrimSpace(bindingKey)
	conversationID = strings.TrimSpace(conversationID)
	if bindingKey == "" || conversationID == "" {
		return
	}
	h.codexTaskCardFocusMu.Lock()
	defer h.codexTaskCardFocusMu.Unlock()
	if h.codexTaskCardFocus == nil {
		h.codexTaskCardFocus = make(map[string]string)
	}
	h.codexTaskCardFocus[bindingKey] = conversationID
}

func (h *Handler) reanchorActiveCodexTask(ctx context.Context, task *activeAgentTask, reply platform.Replier) (bool, error) {
	progress, snapshot, ok := task.progressReanchorSnapshot()
	if !ok {
		return false, nil
	}
	result, err := progress.reanchor(ctx, reply, snapshot, uuid.NewString())
	if result.Moved {
		task.mu.Lock()
		task.trace = traceWithReply(task.trace, progressReplier(reply))
		trace := task.trace
		task.mu.Unlock()
		h.recordTraceStage(trace, "task.card_reanchored", "running", "progress card moved to latest message position")
	}
	if err != nil {
		log.Printf("[codex-session-bind] 任务卡重锚失败 moved=%t: %v", result.Moved, err)
	}
	return result.Moved, err
}

func (h *Handler) failCodexAcquireRuntime(result codexSessionAcquireResult, liveAgent agent.CodexLiveRuntimeAgent, cause error) codexSessionAcquireResult {
	request := result.resolution.Request
	binding, currentErr := liveAgent.CurrentCodexRuntime(request)
	if currentErr == nil {
		result.resolution.Binding = binding
	}
	result.runtimeErr = errors.Join(cause, currentErr)
	return result
}

func renderCodexSessionAcquireFailure(err error) string {
	if err == nil {
		return ""
	}
	log.Printf("[codex-session-bind] 绑定失败: %v", err)
	switch {
	case errors.Is(err, errCodexSessionAcquireActiveOld):
		return "当前会话任务仍在执行，请等待完成或先发送 /stop。"
	case errors.Is(err, errCodexRemoteSelectionChanged):
		return "Codex 会话绑定已被并发修改，请重新查询后重试。"
	case errors.Is(err, errCodexSessionAcquireUncertain):
		return "未切换到 Codex：会话绑定结果未确认。当前窗口仍保持切换前的 Agent。"
	case isCodexSessionControlTimeout(err):
		return "前一项会话操作仍在处理，本次绑定未执行。"
	case errors.Is(err, errCodexSessionAcquireUnsupported):
		return "当前 Codex Agent 不支持共享 app-server 会话绑定。"
	default:
		return "绑定 Codex 会话失败，请重试。"
	}
}
