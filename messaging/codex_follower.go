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

type codexFollowerService struct {
	registry *platform.Registry
	interval time.Duration
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
		registry: registry,
		interval: interval,
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
		h.reconcileCodexFollowers(ctx, service.registry)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) reconcileCodexFollowers(ctx context.Context, registry *platform.Registry) {
	followers, releases := h.ensureCodexSessions().followerRecoverySnapshots()
	if outbox := h.currentTerminalOutbox(); outbox != nil {
		h.recoverReleasedCodexFollowerStreamsForTargets(outbox, committedCodexReleaseTargets(releases))
		outbox.reconcileCodexFollowerHolds(followers, releases)
	}
	for _, snapshot := range followers {
		if err := h.reconcileCodexFollower(ctx, registry, snapshot); err != nil && ctx.Err() == nil {
			log.Printf("[codex-follower] 同步观察失败 route=%q thread=%q revision=%d: %v",
				snapshot.BindingKey, snapshot.Target.ThreadID, snapshot.Revision, err)
		}
	}
}

func (h *Handler) reconcileCodexFollower(ctx context.Context, registry *platform.Registry, snapshot codexFollowerSnapshot) error {
	if !h.ensureCodexSessions().followerMatches(snapshot) {
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
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil {
		return err
	}
	unlock, err := h.lockCodexSessionBinding(ctx, snapshot.BindingKey, "follow")
	if err != nil {
		return err
	}
	defer unlock()
	if !h.ensureCodexSessions().followerMatches(snapshot) {
		return nil
	}
	if prepared.active {
		liveAgent, ok := ag.(agent.CodexLiveRuntimeAgent)
		if !ok {
			return errCodexSessionAcquireUnsupported
		}
		unlockThread, lockErr := h.lockCodexSessionThread(ctx, snapshot.Target.ThreadID, "follow")
		if lockErr != nil {
			return lockErr
		}
		route := codexConversationRoute{
			bindingKey: snapshot.BindingKey, conversationID: snapshot.ConversationID,
			workspaceRoot: snapshot.Target.WorkspaceRoot, threadID: snapshot.Target.ThreadID,
		}
		request := h.buildCodexRuntimeRequestForTurn(route, snapshot.Target.ThreadID)
		_, reconcileErr := liveAgent.ReconcileCodexObservedTurn(ctx, request, prepared.state.CodexThreadState)
		unlockThread()
		if reconcileErr != nil {
			return reconcileErr
		}
	}
	if recoveryErr := h.reconcileCodexFollowerRecoveries(snapshot, prepared.state, reply); recoveryErr != nil {
		log.Printf("[codex-follower] 恢复旧进度卡失败 route=%q thread=%q: %v",
			snapshot.BindingKey, snapshot.Target.ThreadID, recoveryErr)
	}
	if !prepared.active {
		return nil
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		return err
	}
	if !h.activateExternalCodexTaskReservation(reservation) {
		h.cancelExternalCodexTaskReservation(reservation)
		return errExternalCodexTaskReservationConflict
	}
	return nil
}

func (h *Handler) reconcileCodexFollowerRecoveries(snapshot codexFollowerSnapshot, state externalCodexTaskState, reply platform.Replier) error {
	outbox := h.currentTerminalOutbox()
	if outbox == nil {
		return nil
	}
	entries := outbox.heldCodexFollowerRecoveries(snapshot)
	var joined error
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
				if err := h.finalizeHeldCodexFollowerRecovery(outbox, entry, state); err != nil {
					joined = errors.Join(joined, err)
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
	return joined
}

func (h *Handler) finalizeHeldCodexFollowerRecovery(outbox *terminalOutbox, entry *terminalOutboxEntry, state externalCodexTaskState) error {
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
		ResultTitle: entry.ResultTitle, RichResult: entry.RichResult, Text: text, Trace: trace,
	}); err != nil {
		return err
	}
	outbox.releaseHeldCodexFollowerRecovery(entry.ID)
	return nil
}
