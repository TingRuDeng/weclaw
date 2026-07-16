package messaging

import (
	"context"
	"errors"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

const codexSessionAcquireCleanupTimeout = 3 * time.Second

type codexRuntimeConflictRequest struct {
	ctx       context.Context
	liveAgent agent.CodexLiveRuntimeAgent
	change    codexRuntimeIntentChange
	intent    codexControlIntent
}

// applyCodexRuntimeIntentChanges 在所有权提交后同步目标和旧 thread；各项失败互不阻断。
func (h *Handler) applyCodexRuntimeIntentChanges(plan codexSessionAcquirePlan, liveAgent agent.CodexLiveRuntimeAgent) (codexRuntimeResolution, []codexRuntimeIntentChange, error) {
	resolution, targetChange, targetErr := h.acquireCodexTargetRuntime(plan, liveAgent)
	applied := make([]codexRuntimeIntentChange, 0, len(plan.changes)+1)
	if targetChange != nil {
		applied = append(applied, *targetChange)
	}
	var cleanupErr error
	for _, change := range plan.changes {
		_, err := h.handoffCodexRuntimeIntent(codexRuntimeHandoffRequest{
			ctx: plan.request.ctx, liveAgent: liveAgent,
			change: change,
		})
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		applied = append(applied, change)
	}
	return resolution, applied, errors.Join(targetErr, cleanupErr)
}

// acquireCodexTargetRuntime 对幂等目标只读本地绑定；只有 owner remote 显式请求才强制恢复。
func (h *Handler) acquireCodexTargetRuntime(plan codexSessionAcquirePlan, liveAgent agent.CodexLiveRuntimeAgent) (codexRuntimeResolution, *codexRuntimeIntentChange, error) {
	before := plan.snapshot.Target
	after := proposedCodexRemoteSelectionIntent(before, plan.request.route)
	change := codexRuntimeIntentChange{
		threadID: plan.request.route.threadID, route: plan.request.route,
		before: before, after: after,
	}
	if before == after && !plan.request.forceRuntimeHandoff {
		resolution, err := h.currentCodexAcquireTarget(plan, liveAgent)
		return resolution, nil, err
	}
	resolution, err := h.handoffCodexRuntimeIntent(codexRuntimeHandoffRequest{
		ctx: plan.request.ctx, liveAgent: liveAgent,
		change: change,
	})
	if err != nil {
		return resolution, nil, err
	}
	return resolution, &change, nil
}

// currentCodexAcquireTarget 读取已建立的 runtime，不向 Desktop 发起探测。
func (h *Handler) currentCodexAcquireTarget(plan codexSessionAcquirePlan, liveAgent agent.CodexLiveRuntimeAgent) (codexRuntimeResolution, error) {
	request, rollout, err := h.buildCodexRuntimeRequest(plan.request.route, plan.request.route.threadID)
	if err != nil {
		return codexRuntimeResolution{}, err
	}
	binding, err := liveAgent.CurrentCodexRuntime(request)
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

// handoffCodexRuntimeIntent 最多调用一次副作用；失败后直接按已提交意图进入冲突态。
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
	markCtx := req.resyncCtx
	cancel := func() {}
	if markCtx == nil {
		markCtx, cancel = newCodexSessionAcquireCleanupContext(req.ctx)
	}
	defer cancel()
	markErr := req.liveAgent.MarkCodexRuntimeConflict(markCtx, request)
	current, currentErr := req.liveAgent.CurrentCodexRuntime(request)
	if currentErr == nil {
		resolution.Binding = current
	}
	return resolution, errors.Join(handoffErr, markErr, currentErr)
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
			ctx: ctx, resyncCtx: ctx, liveAgent: liveAgent,
			change: reverse,
		})
		if err != nil {
			markErr := h.markCodexRuntimeConflict(codexRuntimeConflictRequest{
				ctx: ctx, liveAgent: liveAgent, change: reverse, intent: reverse.after,
			})
			compensationErr = errors.Join(compensationErr, err, markErr)
		}
	}
	return compensationErr
}

// markCodexRuntimeConflict 为补偿失败的 thread 持续登记 fail-closed 状态。
func (h *Handler) markCodexRuntimeConflict(req codexRuntimeConflictRequest) error {
	request := agent.CodexRuntimeRequest{
		Ref:    req.change.route.ref(req.change.threadID),
		Intent: agentControlIntent(req.intent),
	}
	return req.liveAgent.MarkCodexRuntimeConflict(req.ctx, request)
}

// newCodexSessionAcquireCleanupContext 为首次清理保留 values、脱离父取消并设置有限预算。
func newCodexSessionAcquireCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	parent := context.WithoutCancel(normalizeContext(ctx))
	return context.WithTimeout(parent, codexSessionAcquireCleanupTimeout)
}

// finishCodexOwnerSelection 在所有权落盘后立即切换窗口工作空间和默认 Agent。
func (h *Handler) finishCodexOwnerSelection(request codexSessionAcquireRequest) codexSessionAcquireResult {
	agentSessionErr := h.ensureAgentSessions().Set(
		request.routeUserID, request.agentName,
	)
	if agentSessionErr != nil {
		return codexSessionAcquireResult{route: request.route, agentSessionErr: agentSessionErr}
	}
	h.switchCodexWorkspaceForRoute(
		firstNonBlank(request.actorUserID, request.routeUserID),
		request.routeUserID, request.agentName,
		request.route.workspaceRoot, request.agent,
	)
	return codexSessionAcquireResult{
		route: request.route, agentSessionErr: agentSessionErr,
	}
}
