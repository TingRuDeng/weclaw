package messaging

import (
	"context"
	"errors"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// startCodexAgentTask 先登记 active task 再后台执行，保证 /guide 和 /cancel 可及时进入 Handler。
func (h *Handler) startCodexAgentTask(opts codexAgentTaskOptions) {
	if strings.TrimSpace(opts.routeUserID) == "" {
		opts.routeUserID = opts.userID
	}
	bindingKey := codexBindingKey(opts.routeUserID, opts.agentName)
	unlockBinding := h.lockAgentExecution(codexBindingExecutionKey(bindingKey))
	defer unlockBinding()
	agentCtx, cancelTaskTimeout := contextWithTaskTimeout(opts.ctx, opts.progressCfg)
	agentCtx = h.withAgentInteractions(agentCtx, agentInteractionContextOptions{
		actorUserID: opts.userID, routeUserID: opts.routeUserID, reply: opts.reply,
	})
	route := opts.route
	if route.conversationID == "" {
		route = h.codexConversationRouteForSession(opts.userID, opts.routeUserID, opts.agentName, opts.agent)
	}
	if !h.workspaceAllowedForAgentContext(opts.ctx, opts.agentName, route.workspaceRoot) {
		sendPlatformText(opts.ctx, opts.reply, opts.userID, "当前工作空间不在允许范围，请发送 /cx ls 重新选择。")
		cancelTaskTimeout()
		return
	}
	if h.preflightCodexTaskStart(codexTaskPreflightOptions{
		taskOpts: opts, route: route, cancel: cancelTaskTimeout,
	}) {
		return
	}
	executionKey := route.conversationID
	runtimeOwner, ownerRevision := codexTaskOwnerSnapshot(opts.agent, route.conversationID)
	opts.route = route
	admission := h.beginOrQueueActiveTask(agentCtx, executionKey, activeTaskMeta{
		owner:        opts.userID,
		routeUserID:  opts.routeUserID,
		agentName:    opts.agentName,
		message:      opts.message,
		runtimeOwner: runtimeOwner, ownerRevision: ownerRevision,
		codexThreadID: route.threadID,
	}, h.pendingCodexTask(opts))
	if admission.status != activeTaskStarted {
		cancelTaskTimeout()
		replyAgentTaskAdmission(agentTaskAdmissionNotice{
			ctx: opts.ctx, reply: opts.reply, userID: opts.userID,
		}, admission.status)
		return
	}
	task := admission.task
	taskCtx := admission.taskCtx

	go h.runCodexAgentTask(codexAgentTaskRuntime{
		opts:              opts,
		agentCtx:          taskCtx,
		cancelTaskTimeout: cancelTaskTimeout,
		executionKey:      executionKey,
		route:             route,
		task:              task,
	})
}

// pendingCodexTask 冻结第二条消息的 route 与回复上下文，供上一任务结束后续跑。
func (h *Handler) pendingCodexTask(opts codexAgentTaskOptions) pendingAgentTask {
	opts.ctx = context.WithoutCancel(opts.ctx)
	return pendingAgentTask{
		message:    opts.message,
		codexRoute: opts.route,
		run:        func() { h.startCodexAgentTask(opts) },
	}
}

// runCodexAgentTask 在后台完成 Codex 调用和最终回复发送。
func (h *Handler) runCodexAgentTask(runtime codexAgentTaskRuntime) {
	opts := runtime.opts
	unlock := h.lockAgentExecution(runtime.executionKey)
	lifecycle := h.startAgentTaskLifecycle(agentTaskLifecycleOptions{
		taskCtx: runtime.agentCtx, replyCtx: opts.ctx, reply: opts.reply,
		task: runtime.task, cancel: runtime.cancelTaskTimeout, executionKey: runtime.executionKey,
		userID: opts.userID, agentName: opts.agentName, message: opts.message,
		replyPrefix: opts.replyPrefix, progressConfig: opts.progressCfg,
	})
	defer h.completeAgentTaskLifecycle(lifecycle)
	defer unlock()

	if err := h.prepareCodexConversation(runtime.agentCtx, runtime.route, opts.agent); err != nil {
		h.finishAgentTaskLifecycle(lifecycle, "", err)
		return
	}
	if liveAgent, ok := opts.agent.(agent.CodexLiveRuntimeAgent); ok {
		if binding, found := liveAgent.CurrentCodexThreadBinding(runtime.route.conversationID); found {
			runtime.task.syncCodexRuntime(binding)
		}
	}
	reply, err := h.executeCodexAgentTurn(runtime, lifecycle.recordProgress)
	if err == nil {
		h.recordCodexThreadForWorkspace(opts.routeUserID, opts.agentName, opts.agent, runtime.route.conversationID, runtime.route.workspaceRoot)
	}
	h.finishAgentTaskLifecycle(lifecycle, reply, err)
}

// executeCodexAgentTurn 在观察流中断时接续同一 rollout turn，不重复执行任务。
func (h *Handler) executeCodexAgentTurn(runtime codexAgentTaskRuntime, onProgress func(string)) (string, error) {
	opts := runtime.opts
	reply, err := h.chatWithAgentWithProgress(runtime.agentCtx, opts.agent, runtime.route.conversationID, opts.message, onProgress)
	var interrupted *agent.CodexTurnInterruptedError
	if !errors.As(err, &interrupted) {
		return reply, err
	}
	runtime.task.markCodexObservationInterrupted(interrupted.ThreadID, interrupted.TurnID)
	result := h.reconcileInterruptedCodexTurn(runtime.agentCtx, interrupted, onProgress)
	if result.Terminal && !result.Failed {
		return result.Final, nil
	}
	if result.Err != nil {
		return "", result.Err
	}
	return "", interrupted
}

func codexTaskOwnerSnapshot(ag agent.Agent, conversationID string) (agent.CodexRuntimeOwner, uint64) {
	liveAgent, ok := ag.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return agent.CodexOwnerWeClawRuntime, 0
	}
	binding, found := liveAgent.CurrentCodexThreadBinding(conversationID)
	if !found {
		return agent.CodexOwnerUnknown, 0
	}
	return binding.Owner, binding.OwnerRevision
}
