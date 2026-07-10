package messaging

import (
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
	agentCtx = agent.ContextWithApprovalHandler(agentCtx, h.approvalHandlerForUser(opts.userID, opts.routeUserID, opts.reply))
	route := h.codexConversationRouteForSession(opts.userID, opts.routeUserID, opts.agentName, opts.agent)
	if !h.workspaceAllowedForAgentContext(opts.ctx, opts.agentName, route.workspaceRoot) {
		sendPlatformText(opts.ctx, opts.reply, opts.userID, "当前工作空间不在允许范围，请发送 /cx ls 重新选择。")
		cancelTaskTimeout()
		return
	}
	executionKey := route.conversationID
	task, taskCtx, started := h.beginActiveTask(agentCtx, executionKey, activeTaskMeta{
		owner:     opts.userID,
		agentName: opts.agentName,
		message:   opts.message,
	})
	if !started {
		cancelTaskTimeout()
		if !task.acceptsGuide() {
			sendPlatformText(opts.ctx, opts.reply, opts.userID, runningReadOnlyCodexAppPrompt())
			return
		}
		if h.storePendingGuide(executionKey, opts.message) {
			sendPlatformText(opts.ctx, opts.reply, opts.userID, runningCodexGuidePromptForTask(task))
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

// finishCodexAgentTask 收尾后台任务，并把未处理的暂存引导转成确认消息。
func (h *Handler) finishCodexAgentTask(runtime codexAgentTaskRuntime) {
	runtime.cancelTaskTimeout()
	message, ok := h.completeActiveTask(runtime.executionKey, runtime.task)
	if !ok {
		return
	}
	opts := runtime.opts
	sendPlatformText(opts.ctx, opts.reply, opts.userID, runnablePendingCodexPrompt(message))
}
