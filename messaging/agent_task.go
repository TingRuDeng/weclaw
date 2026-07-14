package messaging

import (
	"context"
	"strings"

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
	opts      agentTaskOptions
	lifecycle agentTaskLifecycle
}

// startAgentTask 先创建进度卡和任务登记，再后台执行 Claude ACP。
func (h *Handler) startAgentTask(opts agentTaskOptions) {
	if strings.TrimSpace(opts.routeUserID) == "" {
		opts.routeUserID = opts.userID
	}
	bindingKey := claudeBindingKey(opts.routeUserID, opts.agentName)
	unlockBinding := h.lockAgentExecution(claudeBindingExecutionKey(bindingKey))
	defer unlockBinding()
	// 后台任务保留消息上下文值，但不能随平台请求返回而被取消。
	opts.ctx = context.WithoutCancel(opts.ctx)
	agentCtx, cancel := contextWithTaskTimeout(opts.ctx, opts.progressCfg)
	agentCtx = h.withAgentInteractions(agentCtx, agentInteractionContextOptions{
		actorUserID: opts.userID, routeUserID: opts.routeUserID, reply: opts.reply,
	})
	key := h.agentExecutionKeyForRoute(opts.userID, opts.routeUserID, opts.agentName, opts.agent)
	pending := pendingAgentTask{message: opts.message, run: func() { h.startAgentTask(opts) }}
	admission := h.beginOrQueueActiveTask(agentCtx, key, activeTaskMeta{
		owner: opts.userID, routeUserID: opts.routeUserID, agentName: opts.agentName, message: opts.message,
	}, pending)
	if admission.status != activeTaskStarted {
		cancel()
		replyAgentTaskAdmission(agentTaskAdmissionNotice{
			ctx: opts.ctx, reply: opts.reply, userID: opts.userID,
		}, admission.status)
		return
	}
	runtime := agentTaskRuntime{
		opts: opts,
		lifecycle: h.startAgentTaskLifecycle(agentTaskLifecycleOptions{
			taskCtx: admission.taskCtx, replyCtx: opts.ctx, reply: opts.reply,
			task: admission.task, cancel: cancel, executionKey: key,
			userID: opts.userID, agentName: opts.agentName, message: opts.message,
			replyPrefix: opts.replyPrefix, progressConfig: opts.progressCfg,
		}),
	}
	go h.runAgentTask(runtime)
}

// runAgentTask 在后台恢复当前会话并持续转发结构化进度。
func (h *Handler) runAgentTask(runtime agentTaskRuntime) {
	defer h.completeAgentTaskLifecycle(runtime.lifecycle)
	unlock := h.lockAgentExecution(runtime.lifecycle.opts.executionKey)
	defer unlock()
	conversationID, err := h.resolveAgentConversationIDForRoute(
		runtime.lifecycle.opts.taskCtx, runtime.opts.userID, runtime.opts.routeUserID,
		runtime.opts.agentName, runtime.opts.agent,
	)
	if err != nil {
		h.finishAgentTaskLifecycle(runtime.lifecycle, "", err)
		return
	}
	reply, err := h.chatWithAgentWithProgress(
		runtime.lifecycle.opts.taskCtx, runtime.opts.agent, conversationID,
		runtime.opts.message, runtime.lifecycle.recordProgress,
	)
	h.finishAgentTaskLifecycle(runtime.lifecycle, reply, err)
}

// isBackgroundClaudeAgent 只让 Claude ACP 使用通用后台任务执行器。
func isBackgroundClaudeAgent(name string, ag agent.Agent) bool {
	return isClaudeAgent(name, ag.Info()) && strings.EqualFold(ag.Info().Type, "acp")
}
