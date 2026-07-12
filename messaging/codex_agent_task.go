package messaging

import (
	"context"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

// startCodexAgentTask 先登记 active task 再后台执行，保证 /guide 和 /cancel 可及时进入 Handler。
func (h *Handler) startCodexAgentTask(opts codexAgentTaskOptions) {
	if strings.TrimSpace(opts.routeUserID) == "" {
		opts.routeUserID = opts.userID
	}
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
	task, taskCtx, started := h.beginActiveTask(agentCtx, executionKey, activeTaskMeta{
		owner:        opts.userID,
		agentName:    opts.agentName,
		message:      opts.message,
		runtimeOwner: runtimeOwner, ownerRevision: ownerRevision,
		codexThreadID: route.threadID,
	})
	if !started {
		cancelTaskTimeout()
		opts.route = route
		if h.storePendingGuide(executionKey, h.pendingCodexTask(opts)) {
			sendPlatformText(opts.ctx, opts.reply, opts.userID, queuedCodexMessage)
		} else {
			sendPlatformText(opts.ctx, opts.reply, opts.userID, "当前任务已有一条暂存消息，请先处理后再发送。")
		}
		return
	}

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
	defer h.finishCodexAgentTask(runtime)

	unlock := h.lockAgentExecution(runtime.executionKey)
	defer unlock()

	onProgress, finishProgress := h.startProgressSessionWithFinal(runtime.agentCtx, opts.reply, opts.replyPrefix, opts.message, opts.progressCfg)
	recordProgress := func(delta string) {
		runtime.task.recordProgress(time.Now(), delta)
		onProgress(delta)
	}

	if err := h.prepareCodexConversation(runtime.agentCtx, runtime.route, opts.agent); err != nil {
		reply := renderFinalFailure(opts.replyPrefix, err)
		consumed := finishProgressWithReplyForPlatform(opts.reply, finishProgress, reply, true)
		h.sendReplyWithMediaAfterStreamForRoute(opts.ctx, opts.reply, opts.userID, opts.routeUserID, opts.agentName, reply, consumed)
		return
	}
	if liveAgent, ok := opts.agent.(agent.CodexLiveRuntimeAgent); ok {
		if binding, found := liveAgent.CurrentCodexThreadBinding(runtime.route.conversationID); found {
			runtime.task.syncCodexRuntime(binding)
		}
	}
	reply, err := h.chatWithAgentWithProgress(runtime.agentCtx, opts.agent, runtime.route.conversationID, opts.message, recordProgress)
	if err != nil {
		reply = renderFinalFailure(opts.replyPrefix, err)
	} else {
		h.recordCodexThreadForWorkspace(opts.routeUserID, opts.agentName, opts.agent, runtime.route.conversationID, runtime.route.workspaceRoot)
		reply = renderFinalSuccess(opts.replyPrefix, reply)
	}
	if runtime.task.shouldSendFinal() {
		consumed := finishProgressWithReplyForPlatform(opts.reply, finishProgress, reply, err != nil)
		h.sendReplyWithMediaAfterStreamForRoute(opts.ctx, opts.reply, opts.userID, opts.routeUserID, opts.agentName, reply, consumed)
	} else {
		_ = finishProgress("", false)
	}
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

// finishCodexAgentTask 收尾后台任务，并自动执行未被消费的暂存消息。
func (h *Handler) finishCodexAgentTask(runtime codexAgentTaskRuntime) {
	runtime.cancelTaskTimeout()
	pending, ok := h.completeActiveTask(runtime.executionKey, runtime.task)
	if !ok {
		return
	}
	pending.run()
}
