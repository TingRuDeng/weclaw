package messaging

import (
	"context"
	"errors"

	"github.com/fastclaw-ai/weclaw/agent"
)

// applyCodexRuntimeIntentChanges 先接管目标，再按计划顺序释放旧 remote thread。
func (h *Handler) applyCodexRuntimeIntentChanges(plan codexSessionAcquirePlan, liveAgent agent.CodexLiveRuntimeAgent) (codexRuntimeResolution, []codexRuntimeIntentChange, error) {
	resolution, targetChange, err := h.acquireCodexTargetRuntime(plan, liveAgent)
	if err != nil {
		return resolution, nil, err
	}
	applied := make([]codexRuntimeIntentChange, 0, len(plan.changes)+1)
	if targetChange != nil {
		applied = append(applied, *targetChange)
	}
	for _, change := range plan.changes {
		_, err := h.handoffCodexRuntimeIntent(codexRuntimeHandoffRequest{
			ctx: plan.request.ctx, liveAgent: liveAgent,
			change: change, resyncIntent: change.before,
		})
		if err != nil {
			return resolution, applied, err
		}
		applied = append(applied, change)
	}
	return resolution, applied, nil
}

// acquireCodexTargetRuntime 对幂等目标只探测，避免重复发出 handoff 副作用。
func (h *Handler) acquireCodexTargetRuntime(plan codexSessionAcquirePlan, liveAgent agent.CodexLiveRuntimeAgent) (codexRuntimeResolution, *codexRuntimeIntentChange, error) {
	before := plan.snapshot.Target
	after := proposedCodexRemoteSelectionIntent(before, plan.request.route)
	change := codexRuntimeIntentChange{
		threadID: plan.request.route.threadID, route: plan.request.route,
		before: before, after: after,
	}
	if before == after {
		resolution, err := h.inspectCodexAcquireTarget(plan, liveAgent)
		return resolution, nil, err
	}
	resolution, err := h.handoffCodexRuntimeIntent(codexRuntimeHandoffRequest{
		ctx: plan.request.ctx, liveAgent: liveAgent,
		change: change, resyncIntent: before,
	})
	if err != nil {
		return resolution, nil, err
	}
	return resolution, &change, nil
}

// inspectCodexAcquireTarget 读取幂等目标的实时运行位置和活动状态。
func (h *Handler) inspectCodexAcquireTarget(plan codexSessionAcquirePlan, liveAgent agent.CodexLiveRuntimeAgent) (codexRuntimeResolution, error) {
	request, rollout, err := h.buildCodexRuntimeRequest(plan.request.route, plan.request.route.threadID)
	if err != nil {
		return codexRuntimeResolution{}, err
	}
	binding, err := liveAgent.InspectCodexRuntime(plan.request.ctx, request)
	return codexRuntimeResolution{
		Request: request, Binding: binding, Rollout: rollout,
		Live: true, ProbeErr: err,
	}, err
}

// proposedCodexRemoteSelectionIntent 仅在所有权三元组变化时推进 revision。
func proposedCodexRemoteSelectionIntent(current codexControlIntent, route codexConversationRoute) codexControlIntent {
	if current.Owner == codexControlRemote &&
		current.RouteBindingKey == route.bindingKey &&
		current.ConversationID == route.conversationID {
		return current
	}
	return codexControlIntent{
		Owner: codexControlRemote, RouteBindingKey: route.bindingKey,
		ConversationID: route.conversationID, Revision: current.Revision + 1,
	}
}

// handoffCodexRuntimeIntent 最多调用一次副作用；超时后仅用持久化意图校准。
func (h *Handler) handoffCodexRuntimeIntent(req codexRuntimeHandoffRequest) (codexRuntimeResolution, error) {
	request, rollout, err := h.buildCodexRuntimeRequest(req.change.route, req.change.threadID)
	if err != nil {
		return codexRuntimeResolution{}, err
	}
	request.Intent = agentControlIntent(req.change.after)
	binding, handoffErr := req.liveAgent.HandoffCodexRuntime(req.ctx, request)
	resolution := codexRuntimeResolution{
		Request: request, Binding: binding, Rollout: rollout,
		Live: true, ProbeErr: handoffErr,
	}
	if handoffErr == nil {
		return resolution, nil
	}
	if !isCodexSessionControlTimeout(handoffErr) {
		return resolution, handoffErr
	}
	request.Intent = agentControlIntent(req.resyncIntent)
	_, inspectErr := req.liveAgent.InspectCodexRuntime(req.ctx, request)
	if inspectErr != nil {
		return resolution, errors.Join(errCodexSessionAcquireUncertain, handoffErr, inspectErr)
	}
	return resolution, handoffErr
}

// compensateCodexRuntimeChanges 按已应用副作用的逆序恢复持久化权威意图。
func (h *Handler) compensateCodexRuntimeChanges(ctx context.Context, liveAgent agent.CodexLiveRuntimeAgent, applied []codexRuntimeIntentChange) error {
	var compensationErr error
	for index := len(applied) - 1; index >= 0; index-- {
		change := applied[index]
		reverse := codexRuntimeIntentChange{
			threadID: change.threadID, route: change.route,
			before: change.after, after: change.before,
		}
		_, err := h.handoffCodexRuntimeIntent(codexRuntimeHandoffRequest{
			ctx: ctx, liveAgent: liveAgent,
			change: reverse, resyncIntent: reverse.after,
		})
		if err != nil {
			compensationErr = errors.Join(compensationErr, err)
		}
	}
	return compensationErr
}

// finishCodexSessionAcquire 激活观察后再更新不参与事务提交的辅助会话状态。
func (h *Handler) finishCodexSessionAcquire(commit codexSessionAcquireCommit) codexSessionAcquireResult {
	intent := agentControlIntent(commit.committed.Target)
	commit.resolution.Request.Intent = intent
	commit.resolution.Binding.Control = intent
	h.activateExternalCodexTaskReservation(commit.reservation)
	h.switchCodexWorkspaceForRoute(
		firstNonBlank(commit.request.actorUserID, commit.request.routeUserID),
		commit.request.routeUserID, commit.request.agentName,
		commit.request.route.workspaceRoot, commit.request.agent,
	)
	agentSessionErr := h.ensureAgentSessions().Set(
		commit.request.routeUserID, commit.request.agentName,
	)
	return codexSessionAcquireResult{
		route: commit.request.route, resolution: commit.resolution,
		externalState: commit.prepared.state, externalActive: commit.prepared.active,
		agentSessionErr: agentSessionErr,
	}
}

// rollbackCodexAcquire 先撤销观察 reservation，再逆序补偿运行时副作用。
func (h *Handler) rollbackCodexAcquire(rollback codexSessionAcquireRollback) error {
	h.cancelExternalCodexTaskReservation(rollback.reservation)
	if err := h.compensateCodexRuntimeChanges(
		rollback.plan.request.ctx, rollback.liveAgent, rollback.applied,
	); err != nil {
		return errors.Join(errCodexSessionAcquireUncertain, rollback.cause, err)
	}
	return rollback.cause
}
