package messaging

import (
	"context"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

type agentTaskOptions struct {
	ctx          context.Context
	platformName platform.PlatformName
	accountID    string
	userID       string
	routeUserID  string
	reply        platform.Replier
	agentName    string
	message      string
	replyPrefix  string
	agent        agent.Agent
	progressCfg  config.ProgressConfig
}

type agentTaskRuntime struct {
	opts              agentTaskOptions
	agentCtx          context.Context
	cancelTaskTimeout context.CancelFunc
	executionKey      string
	task              *activeAgentTask
	onProgress        func(string)
	finishProgress    func(string, bool) bool
}

// startAgentTask 先创建进度卡和任务登记，再后台执行 Claude ACP。
func (h *Handler) startAgentTask(opts agentTaskOptions) {
	if strings.TrimSpace(opts.routeUserID) == "" {
		opts.routeUserID = opts.userID
	}
	// 后台任务保留消息上下文值，但不能随平台请求返回而被取消。
	opts.ctx = context.WithoutCancel(opts.ctx)
	agentCtx, cancel := contextWithTaskTimeout(opts.ctx, opts.progressCfg)
	agentCtx = h.withAgentInteractions(agentCtx, agentInteractionContextOptions{
		actorUserID: opts.userID, routeUserID: opts.routeUserID, reply: opts.reply,
	})
	key := h.agentExecutionKeyForRoute(opts.userID, opts.routeUserID, opts.agentName, opts.agent)
	task, taskCtx, started := h.beginActiveTask(agentCtx, key, activeTaskMeta{
		owner: opts.userID, agentName: opts.agentName, message: opts.message,
	})
	if !started {
		cancel()
		h.queueAgentTask(opts, key)
		return
	}
	onProgress, finish := h.startProgressSessionWithFinal(taskCtx, opts.reply, opts.replyPrefix, opts.message, opts.progressCfg)
	runtime := agentTaskRuntime{
		opts: opts, agentCtx: taskCtx, cancelTaskTimeout: cancel,
		executionKey: key, task: task, onProgress: onProgress, finishProgress: finish,
	}
	go h.runAgentTask(runtime)
}

// queueAgentTask 每个活动任务最多暂存一条后续消息。
func (h *Handler) queueAgentTask(opts agentTaskOptions, key string) {
	pending := pendingAgentTask{message: opts.message, run: func() { h.startAgentTask(opts) }}
	if h.storePendingGuide(key, pending) {
		sendPlatformText(opts.ctx, opts.reply, opts.userID, queuedAgentMessage)
		return
	}
	sendPlatformText(opts.ctx, opts.reply, opts.userID, "当前任务已有一条暂存消息，请先处理后再发送。")
}

// runAgentTask 在后台恢复当前会话并持续转发结构化进度。
func (h *Handler) runAgentTask(runtime agentTaskRuntime) {
	defer h.finishAgentTask(runtime)
	unlock := h.lockAgentExecution(runtime.executionKey)
	defer unlock()
	recordProgress := func(delta string) {
		runtime.task.recordProgress(time.Now(), delta)
		runtime.onProgress(delta)
	}
	conversationID, err := h.resolveAgentConversationIDForRoute(
		runtime.agentCtx, runtime.opts.userID, runtime.opts.routeUserID,
		runtime.opts.agentName, runtime.opts.agent,
	)
	if err != nil {
		h.finishAgentTaskReply(runtime, "", err)
		return
	}
	reply, err := h.chatWithAgentWithProgress(
		runtime.agentCtx, runtime.opts.agent, conversationID, runtime.opts.message, recordProgress,
	)
	h.finishAgentTaskReply(runtime, reply, err)
}

// finishAgentTaskReply 根据任务终态更新已有进度卡，避免重复发送正文。
func (h *Handler) finishAgentTaskReply(runtime agentTaskRuntime, reply string, err error) {
	if err != nil {
		reply = renderFinalFailure(runtime.opts.replyPrefix, err)
	} else {
		reply = renderFinalSuccess(runtime.opts.replyPrefix, reply)
	}
	if runtime.task.shouldSendFinal() {
		h.finishAndSendProgressReply(progressReplyDelivery{
			delivery: replyDeliveryRequest{
				ctx: runtime.opts.ctx, replyWriter: runtime.opts.reply, userID: runtime.opts.userID,
				agentName: runtime.opts.agentName, reply: reply,
			},
			failed: err != nil, finish: runtime.finishProgress,
		})
		return
	}
	_ = runtime.finishProgress("", false)
}

// finishAgentTask 清理活动任务，并无条件执行已经排队的下一条消息。
func (h *Handler) finishAgentTask(runtime agentTaskRuntime) {
	runtime.cancelTaskTimeout()
	if pending, ok := h.completeActiveTask(runtime.executionKey, runtime.task); ok {
		pending.run()
	}
}

// isBackgroundClaudeAgent 只让 Claude ACP 使用通用后台任务执行器。
func isBackgroundClaudeAgent(name string, ag agent.Agent) bool {
	return isClaudeAgent(name, ag.Info()) && strings.EqualFold(ag.Info().Type, "acp")
}
