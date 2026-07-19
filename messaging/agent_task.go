package messaging

import (
	"context"
	"errors"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

type agentTaskOptions struct {
	ctx           context.Context
	platformName  platform.PlatformName
	accountID     string
	userID        string
	routeUserID   string
	reply         platform.Replier
	agentName     string
	workspaceRoot string
	message       string
	replyPrefix   string
	agent         agent.Agent
	progressCfg   config.ProgressConfig
	claudeBinding claudeTaskBindingSnapshot
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
	store := h.ensureClaudeSessions()
	_, hasBinding := store.bindingSnapshot(bindingKey)
	_, sessionCapable := opts.agent.(agent.ClaudeSessionAgent)
	if hasBinding || sessionCapable {
		unlockBinding := h.lockAgentExecution(claudeBindingExecutionKey(bindingKey))
		defer unlockBinding()
		binding, bindingErr := store.requireWritableBinding(bindingKey)
		if bindingErr != nil {
			sendPlatformText(opts.ctx, opts.reply, opts.userID, renderClaudeBindingError(bindingErr))
			return
		}
		opts.claudeBinding = claudeTaskBindingSnapshot{SessionID: binding.SessionID, Revision: binding.Revision}
		opts.workspaceRoot = firstNonBlank(binding.WorkspaceRoot, h.claudeWorkspaceRootForUser(opts.userID, opts.agentName, opts.agent))
	}
	// 后台任务保留消息上下文值，但不能随平台请求返回而被取消。
	opts.ctx = context.WithoutCancel(opts.ctx)
	agentCtx, cancel := contextWithTaskTimeout(opts.ctx, opts.progressCfg)
	agentCtx = h.withAgentInteractions(agentCtx, agentInteractionContextOptions{
		actorUserID: opts.userID, routeUserID: opts.routeUserID,
		agentName: opts.agentName, reply: opts.reply,
	})
	key := h.agentExecutionKeyForRoute(opts.userID, opts.routeUserID, opts.agentName, opts.agent)
	pending := pendingAgentTask{message: opts.message, run: func() { h.startAgentTask(opts) }}
	admission := h.beginOrQueueClaudeTask(agentCtx, key, activeTaskMeta{
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
			userID: opts.userID, agentName: opts.agentName, workspaceRoot: opts.workspaceRoot, message: opts.message,
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
	if runtime.opts.claudeBinding.SessionID != "" {
		bindingKey := claudeBindingKey(runtime.opts.routeUserID, runtime.opts.agentName)
		if bindingErr := h.ensureClaudeSessions().validateBindingSnapshot(bindingKey, runtime.opts.claudeBinding); bindingErr != nil {
			h.finishAgentTaskLifecycle(runtime.lifecycle, "", errors.New(renderClaudeBindingError(bindingErr)))
			return
		}
	}
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
