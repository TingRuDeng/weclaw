package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
)

type codexLeasedTurnOptions struct {
	binding  CodexThreadBinding
	request  CodexTurnRequest
	lease    *codexWriterLease
	permit   *codexAppServerPermit
	baseline codexInputDeliveryBaseline
}

const codexTurnStateRaceRetries = 2

// RunCodexTurn reads the authoritative thread state for each input. Active
// turns receive turn/steer; idle turns receive turn/start. Deterministic state
// races are re-read, while unknown delivery is never retried blindly.
func (a *ACPAgent) RunCodexTurn(ctx context.Context, req CodexTurnRequest) (string, error) {
	return a.runCodexTurn(ctx, req, codexTurnStateRaceRetries)
}

// SteerCodexInput reads the selected thread runtime's authoritative state and submits one
// input only when the thread is active. It never starts a new turn or creates a
// second observer; callers can fall back to RunCodexTurn after ErrCodexNoActiveTurn.
func (a *ACPAgent) SteerCodexInput(ctx context.Context, req CodexTurnRequest) (string, error) {
	a.codexAdmissionMu.Lock()
	admissionLocked := true
	defer func() {
		if admissionLocked {
			a.codexAdmissionMu.Unlock()
		}
	}()

	binding, err := a.resolveCodexRuntimeForInputLocked(ctx, req.Runtime)
	if err != nil {
		return "", err
	}

	var permit *codexAppServerPermit
	if a.protocol == protocolCodexAppServer {
		permit, err = a.ensureCodexAppServerGate().acquire(ctx)
		if err != nil {
			return "", err
		}
		defer permit.release()
	}
	binding, err = a.prepareCodexRuntimeForWrite(ctx, req.Runtime, binding)
	if err != nil {
		return "", err
	}
	a.codexAdmissionMu.Unlock()
	admissionLocked = false

	state := binding.State
	if !state.Active {
		return "", ErrCodexNoActiveTurn
	}
	return a.submitCodexSteer(ctx, req, state, codexTurnStateRaceRetries)
}

func (a *ACPAgent) runCodexTurn(ctx context.Context, req CodexTurnRequest, raceRetries int) (string, error) {
	a.codexAdmissionMu.Lock()
	admissionLocked := true
	defer func() {
		if admissionLocked {
			a.codexAdmissionMu.Unlock()
		}
	}()
	binding, err := a.resolveCodexRuntimeForInputLocked(ctx, req.Runtime)
	if err != nil {
		req, binding, err = a.replaceMissingFirstTurnThread(ctx, req, err)
		if err != nil {
			return "", err
		}
	}
	// Runtime discovery, account reconciliation, and handoff may restart the
	// shared Host by draining this gate. Admit the turn only after those
	// maintenance paths finish, then acquire the writer lease while the permit
	// is held so account switching cannot observe a lease-free admitted turn.
	var permit *codexAppServerPermit
	if a.protocol == protocolCodexAppServer {
		permit, err = a.ensureCodexAppServerGate().acquire(ctx)
		if err != nil {
			return "", err
		}
		defer permit.release()
	}
	binding, err = a.prepareCodexRuntimeForWrite(ctx, req.Runtime, binding)
	if err != nil {
		return "", err
	}
	if binding.State.Active {
		a.codexAdmissionMu.Unlock()
		admissionLocked = false
		return a.steerAndObserveCodexTurn(ctx, req, binding.State, raceRetries)
	}
	lease, err := a.codexOwners.beginTurn(req.Runtime)
	if err != nil {
		if errors.Is(err, ErrCodexWriterBusy) && raceRetries > 0 {
			resolved, exists := a.codexOwners.writerLeaseResolution(req.Runtime.Ref.ThreadID)
			a.codexAdmissionMu.Unlock()
			admissionLocked = false
			if exists {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-resolved:
				}
			}
			return a.redispatchCodexTurn(ctx, req, raceRetries-1)
		}
		return "", err
	}
	a.codexAdmissionMu.Unlock()
	admissionLocked = false
	retainLease := false
	defer func() {
		if !retainLease {
			lease.finish()
		}
	}()
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go cancelCodexTurnOnConflict(turnCtx, cancel, lease.conflictSignal())
	baseline := newCodexStartDeliveryBaseline(req, binding.State)
	if err := notifyCodexInputAttempt(req, baseline, CodexInputAttemptPending); err != nil {
		return "", err
	}

	reply, runErr := a.runCodexTurnWithLease(turnCtx, codexLeasedTurnOptions{
		binding: binding, request: req, lease: lease, permit: permit, baseline: baseline,
	})
	if leaseErr := lease.check(); leaseErr != nil {
		return "", leaseErr
	}
	if isCodexInputDeliveryUnknown(runErr) {
		turnID, confirmErr := a.confirmCodexInputDelivery(ctx, req, baseline)
		if confirmErr != nil {
			if err := notifyCodexInputAttempt(req, baseline, CodexInputAttemptUnconfirmed); err != nil {
				log.Printf("[codex-input] 保存未知交付状态失败 thread=%q: %v", req.Runtime.Ref.ThreadID, err)
			}
			return "", confirmErr
		}
		if err := lease.accept(turnID); err != nil {
			return "", err
		}
		if err := notifyCodexInputAttempt(req, baseline, CodexInputAttemptAccepted); err != nil {
			log.Printf("[codex-input] 保存已确认交付状态失败 thread=%q turn=%q: %v", req.Runtime.Ref.ThreadID, turnID, err)
		}
		if req.OnTurnStarted != nil {
			if err := req.OnTurnStarted(req.Runtime.Ref, turnID); err != nil {
				return "", err
			}
		}
		reply, runErr = a.WatchCodexThreadEventsForTurn(
			ctx, req.Runtime.Ref.ConversationID, req.Runtime.Ref.ThreadID, turnID, req.OnProgressEvent,
		)
	}
	if runErr != nil && raceRetries > 0 && isCodexTurnStateRace(runErr) {
		lease.finish()
		return a.redispatchCodexTurn(ctx, req, raceRetries-1)
	}
	if runErr != nil && lease.acceptedTurnID() == "" {
		recordCodexRejectedInputAttempt(req, baseline)
	}
	var interrupted *CodexTurnInterruptedError
	if errors.As(runErr, &interrupted) {
		lease.markUncertain()
		interrupted.setTerminalConfirmation(lease.finish)
		retainLease = true
	}
	return reply, runErr
}

func (a *ACPAgent) resolveCodexRuntimeForInputLocked(
	ctx context.Context,
	req CodexRuntimeRequest,
) (CodexThreadBinding, error) {
	// A private Desktop binding always stays on its verified IPC transport. It
	// must never fall through to app-server startup merely because this route is
	// not the one that originally populated the runtime cache.
	if a.desktopProbe != nil && !a.codexDesktopHostSelection {
		if current, err := a.CurrentCodexRuntime(req); err == nil && current.Runtime == CodexRuntimeDesktop {
			if a.desktopRuntime == nil {
				return CodexThreadBinding{}, ErrCodexRuntimeUnavailable
			}
			if err := a.desktopProbe.LoadHistory(ctx, req.Ref); err != nil {
				return CodexThreadBinding{}, err
			}
			state, stateErr := a.desktopRuntime.threadState(req.Ref.ThreadID)
			state, stateErr = validateCodexThreadSelectionState(req.Ref.ThreadID, state, stateErr)
			if stateErr != nil {
				return CodexThreadBinding{}, stateErr
			}
			current.Ref = req.Ref
			current.State = state
			a.codexOwners.bindConversation(req.Ref, current)
			return current, nil
		}
	}

	var (
		binding CodexThreadBinding
		err     error
	)
	if a.desktopProbe == nil || !a.codexDesktopHostSelection {
		if a.desktopProbe != nil {
			if err := a.reconcileCodexHostTopologyLocked(ctx); err != nil {
				return CodexThreadBinding{}, err
			}
		}
		if a.usesCodexSharedHost() {
			if err := a.ensureStarted(ctx); err != nil {
				return CodexThreadBinding{}, err
			}
		}
		if err := a.ensureCodexAccountForTurn(ctx); err != nil {
			return CodexThreadBinding{}, err
		}
		preparation, prepareErr := a.prepareCodexThreadProviderLocked(ctx, req)
		if prepareErr != nil {
			return CodexThreadBinding{}, prepareErr
		}
		if preparation.Deferred {
			return CodexThreadBinding{}, errCodexProviderMigrationDeferred
		}
		binding, err = a.activateSharedCodexHost(ctx, req)
	} else {
		preparation, prepareErr := a.prepareCodexThreadProviderLocked(ctx, req)
		if prepareErr != nil {
			return CodexThreadBinding{}, prepareErr
		}
		if preparation.Deferred {
			return CodexThreadBinding{}, errCodexProviderMigrationDeferred
		}
		binding, err = a.inspectCodexRuntimeLocked(ctx, req)
	}
	if err != nil {
		return binding, err
	}
	if a.desktopProbe != nil && (binding.Runtime == CodexRuntimeUnknown || binding.Runtime == CodexRuntimeConflict) {
		return a.handoffCodexRuntimeLocked(ctx, req)
	}
	return binding, nil
}

func (a *ACPAgent) prepareCodexRuntimeForWrite(
	ctx context.Context,
	req CodexRuntimeRequest,
	binding CodexThreadBinding,
) (CodexThreadBinding, error) {
	if binding.Runtime != CodexRuntimeWeClaw {
		if strings.EqualFold(binding.State.ThreadStatus, "systemError") {
			return binding, fmt.Errorf("%w: Codex thread 处于 systemError", ErrCodexRuntimeUnavailable)
		}
		return binding, nil
	}
	a.codexSubscriptionMu.Lock()
	defer a.codexSubscriptionMu.Unlock()
	needsResume := strings.EqualFold(binding.State.ThreadStatus, "notLoaded")
	if strings.EqualFold(binding.State.ThreadStatus, "systemError") {
		return binding, fmt.Errorf("%w: Codex thread 处于 systemError", ErrCodexRuntimeUnavailable)
	}
	if !needsResume {
		return binding, nil
	}
	if err := a.resumeThread(ctx, req.Ref.ConversationID, req.Ref.ThreadID); err != nil {
		if recovered, ok := a.recoverCodexDesktopActiveWriter(ctx, req, err); ok {
			return recovered, nil
		}
		return binding, fmt.Errorf("写入前恢复 Codex thread: %w", err)
	}
	a.bindCodexAppServerThread(req.Ref.ConversationID, req.Ref.ThreadID)
	state, _, err := a.readCodexAppServerThreadStateResult(ctx, req.Ref.ThreadID)
	if err != nil {
		return binding, err
	}
	if strings.EqualFold(state.ThreadStatus, "systemError") || strings.EqualFold(state.ThreadStatus, "notLoaded") {
		return binding, fmt.Errorf("%w: Codex thread 状态为 %s", ErrCodexRuntimeUnavailable, state.ThreadStatus)
	}
	return a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, state)
}

// recoverCodexDesktopActiveWriter handles the Codex App Code Mode topology:
// the official daemon can read the stored thread, while the App's verified
// follower IPC is the only transport that can steer the process holding the
// thread writer lock. No other resume failure is eligible for this fallback.
func (a *ACPAgent) recoverCodexDesktopActiveWriter(
	ctx context.Context,
	req CodexRuntimeRequest,
	resumeErr error,
) (CodexThreadBinding, bool) {
	if !a.codexDesktopCoordination || a.desktopProbe == nil || a.desktopRuntime == nil ||
		!strings.Contains(strings.ToLower(resumeErr.Error()), "already has an active writer") {
		return CodexThreadBinding{}, false
	}
	if err := a.desktopProbe.LoadHistoryForActiveWriter(ctx, req.Ref); err != nil {
		return CodexThreadBinding{}, false
	}
	state, err := a.desktopRuntime.threadState(req.Ref.ThreadID)
	state, err = validateCodexThreadSelectionState(req.Ref.ThreadID, state, err)
	if err != nil || !state.Active || strings.TrimSpace(state.ActiveTurnID) == "" {
		return CodexThreadBinding{}, false
	}
	binding, err := a.codexOwners.activateRuntime(req, CodexRuntimeDesktop, state)
	if err != nil {
		return CodexThreadBinding{}, false
	}
	log.Printf("[codex-runtime] Codex App Code Mode owns active writer; using verified Desktop follower thread=%q turn=%q",
		req.Ref.ThreadID, state.ActiveTurnID)
	return binding, true
}

func (a *ACPAgent) redispatchCodexTurn(ctx context.Context, req CodexTurnRequest, raceRetries int) (string, error) {
	state, err := a.ReadCodexThreadState(ctx, req.Runtime.Ref.ConversationID, req.Runtime.Ref.ThreadID)
	if err != nil {
		return "", err
	}
	if state.Active {
		return a.steerAndObserveCodexTurn(ctx, req, state, raceRetries)
	}
	return a.runCodexTurn(ctx, req, raceRetries)
}

func (a *ACPAgent) steerAndObserveCodexTurn(
	ctx context.Context,
	req CodexTurnRequest,
	state CodexThreadState,
	raceRetries int,
) (string, error) {
	turnID, err := a.submitCodexSteer(ctx, req, state, raceRetries)
	if errors.Is(err, ErrCodexNoActiveTurn) && raceRetries > 0 {
		return a.runCodexTurn(ctx, req, raceRetries-1)
	}
	if err != nil {
		return "", err
	}
	return a.WatchCodexThreadEventsForTurn(
		ctx, req.Runtime.Ref.ConversationID, req.Runtime.Ref.ThreadID, turnID, req.OnProgressEvent,
	)
}

func (a *ACPAgent) submitCodexSteer(
	ctx context.Context,
	req CodexTurnRequest,
	state CodexThreadState,
	raceRetries int,
) (string, error) {
	if !state.Active {
		return "", ErrCodexNoActiveTurn
	}
	turnID := strings.TrimSpace(state.ActiveTurnID)
	if turnID == "" {
		if raceRetries <= 0 {
			return "", fmt.Errorf("%w: active thread did not expose a turn id", ErrCodexRuntimeUnavailable)
		}
		return a.rereadAndSubmitCodexSteer(ctx, req, raceRetries-1)
	}
	baseline, err := a.captureCodexSteerDeliveryBaseline(ctx, req, state)
	if err != nil {
		return "", err
	}
	if err := notifyCodexInputAttempt(req, baseline, CodexInputAttemptPending); err != nil {
		return "", err
	}
	err = a.SteerCodexThread(
		ctx, req.Runtime.Ref.ConversationID, req.Runtime.Ref.ThreadID, turnID, req.Message,
	)
	if err != nil {
		if isCodexInputDeliveryUnknown(err) {
			confirmedTurnID, confirmErr := a.confirmCodexInputDelivery(ctx, req, baseline)
			if confirmErr != nil {
				if persistErr := notifyCodexInputAttempt(req, baseline, CodexInputAttemptUnconfirmed); persistErr != nil {
					log.Printf("[codex-input] 保存未知交付状态失败 thread=%q: %v", req.Runtime.Ref.ThreadID, persistErr)
				}
				return "", confirmErr
			}
			turnID = confirmedTurnID
		} else {
			if raceRetries > 0 && isCodexTurnStateRace(err) {
				return a.rereadAndSubmitCodexSteer(ctx, req, raceRetries-1)
			}
			recordCodexRejectedInputAttempt(req, baseline)
			return "", err
		}
	}
	if err := notifyCodexInputAttempt(req, baseline, CodexInputAttemptAccepted); err != nil {
		log.Printf("[codex-input] 保存已确认交付状态失败 thread=%q turn=%q: %v", req.Runtime.Ref.ThreadID, turnID, err)
	}
	if req.OnTurnSteered != nil {
		if err := req.OnTurnSteered(req.Runtime.Ref, turnID); err != nil {
			return "", err
		}
	}
	return turnID, nil
}

func recordCodexRejectedInputAttempt(req CodexTurnRequest, baseline codexInputDeliveryBaseline) {
	if err := notifyCodexInputAttempt(req, baseline, CodexInputAttemptRejected); err != nil {
		log.Printf("[codex-input] 保存明确拒绝状态失败 thread=%q: %v", req.Runtime.Ref.ThreadID, err)
	}
}

func (a *ACPAgent) rereadAndSubmitCodexSteer(
	ctx context.Context,
	req CodexTurnRequest,
	raceRetries int,
) (string, error) {
	state, err := a.ReadCodexThreadState(ctx, req.Runtime.Ref.ConversationID, req.Runtime.Ref.ThreadID)
	if err != nil {
		return "", err
	}
	if !state.Active {
		return "", ErrCodexNoActiveTurn
	}
	return a.submitCodexSteer(ctx, req, state, raceRetries)
}

func isCodexTurnStateRace(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"already has an active turn",
		"already active",
		"no active turn",
		"expected turn id",
		"expectedturnid",
		"active turn changed",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// replaceMissingFirstTurnThread 仅在 app-server 明确确认旧 thread 不存在且外层允许首写补建时创建替代 thread。
func (a *ACPAgent) replaceMissingFirstTurnThread(
	ctx context.Context,
	req CodexTurnRequest,
	recoveryErr error,
) (CodexTurnRequest, CodexThreadBinding, error) {
	if !req.Runtime.PendingFirstTurn || req.OnThreadReplaced == nil || !isMissingThreadError(recoveryErr) {
		return req, CodexThreadBinding{}, recoveryErr
	}
	previous := req.Runtime.Ref
	threadID, err := a.createThread(ctx, previous.ConversationID)
	if err != nil {
		return req, CodexThreadBinding{}, fmt.Errorf("补建 Codex 首次写入 thread: %w", err)
	}
	current := CodexThreadRef{ConversationID: previous.ConversationID, ThreadID: threadID}
	if err := req.OnThreadReplaced(previous, current); err != nil {
		return req, CodexThreadBinding{}, fmt.Errorf("提交 Codex 首次写入 thread 替换: %w", err)
	}
	req.Runtime.Ref = current
	req.Runtime.Checkpoint = CodexRolloutCheckpoint{}
	req.Runtime.PendingFirstTurn = true
	binding, err := a.codexOwners.activateRuntime(
		req.Runtime, CodexRuntimeWeClaw, CodexThreadState{ThreadID: threadID},
	)
	if err != nil {
		return req, binding, err
	}
	a.persistState()
	return req, binding, nil
}

func (a *ACPAgent) runCodexTurnWithLease(ctx context.Context, opts codexLeasedTurnOptions) (string, error) {
	req := opts.request
	onStarted := func(turnID string) error {
		if err := opts.lease.accept(turnID); err != nil {
			return err
		}
		if err := notifyCodexInputAttempt(req, opts.baseline, CodexInputAttemptAccepted); err != nil {
			log.Printf("[codex-input] 保存已确认交付状态失败 thread=%q turn=%q: %v", req.Runtime.Ref.ThreadID, turnID, err)
		}
		if req.OnTurnStarted != nil {
			return req.OnTurnStarted(req.Runtime.Ref, turnID)
		}
		return nil
	}
	if a.desktopProbe == nil {
		return a.chatCodexAppServerControlledTurn(codexAppServerTurnOptions{
			ctx: ctx, conversationID: req.Runtime.Ref.ConversationID,
			message: req.Message, onProgress: req.OnProgress, onProgressEvent: req.OnProgressEvent, onStarted: onStarted,
			permit: opts.permit,
		})
	}
	switch opts.binding.Runtime {
	case CodexRuntimeDesktop:
		return a.chatCodexDesktopTurn(codexDesktopTurnOptions{
			ctx: ctx, binding: opts.binding, message: req.Message,
			onProgress: req.OnProgress, onProgressEvent: req.OnProgressEvent, onStarted: onStarted,
		})
	case CodexRuntimeWeClaw:
		return a.chatCodexAppServerControlledTurn(codexAppServerTurnOptions{
			ctx: ctx, conversationID: req.Runtime.Ref.ConversationID,
			message: req.Message, onProgress: req.OnProgress, onProgressEvent: req.OnProgressEvent, onStarted: onStarted,
			permit: opts.permit,
		})
	case CodexRuntimeConflict:
		return "", ErrCodexRuntimeConflict
	default:
		return "", fmt.Errorf("%w: %s", ErrCodexRuntimeUnavailable, opts.binding.Runtime)
	}
}

func cancelCodexTurnOnConflict(ctx context.Context, cancel context.CancelFunc, conflict <-chan struct{}) {
	select {
	case <-ctx.Done():
	case <-conflict:
		cancel()
	}
}
