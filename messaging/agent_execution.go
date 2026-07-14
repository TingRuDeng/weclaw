package messaging

import (
	"context"
	"fmt"
	"log"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

// agentMessageRequest 统一承载 Agent 消息，避免默认与指定 Agent 入口长期分叉。
type agentMessageRequest struct {
	ctx          context.Context
	platformName platform.PlatformName
	accountID    string
	userID       string
	routeUserID  string
	reply        platform.Replier
	name         string
	message      string
	clientID     string
}

type synchronousAgentRuntime struct {
	req         agentMessageRequest
	agent       agent.Agent
	prefix      string
	replyCtx    context.Context
	progressCfg config.ProgressConfig
	lifecycle   agentTaskLifecycle
}

type agentDispatchRuntime struct {
	req         agentMessageRequest
	agent       agent.Agent
	prefix      string
	progressCfg config.ProgressConfig
}

type synchronousAgentResult struct {
	runtime synchronousAgentRuntime
	reply   string
	err     error
}

// sendToDefaultAgent 解析当前窗口默认 Agent，并按其运行时能力分发消息。
func (h *Handler) sendToDefaultAgent(req agentMessageRequest) {
	req.name = h.defaultAgentNameForRoute(req.routeUserID, req.platformName, req.accountID)
	if req.name == "" {
		h.sendDefaultAgentEcho(req, nil)
		return
	}
	ag, err := h.getAgent(req.ctx, req.name)
	if err != nil {
		h.sendDefaultAgentEcho(req, err)
		return
	}
	h.dispatchAgentMessage(req, ag, "")
}

// sendToNamedAgent 向指定 Agent 发送消息，并保留名称前缀用于多 Agent 场景。
func (h *Handler) sendToNamedAgent(req agentMessageRequest) {
	ag, err := h.getAgent(req.ctx, req.name)
	if err != nil {
		log.Printf("[handler] agent %q not available: %v", req.name, err)
		sendPlatformText(req.ctx, req.reply, req.userID, fmt.Sprintf("Agent %q is not available: %v", req.name, err))
		return
	}
	h.dispatchAgentMessage(req, ag, "["+req.name+"] ")
}

// sendDefaultAgentEcho 在默认 Agent 尚不可用时保留原有回显行为。
func (h *Handler) sendDefaultAgentEcho(req agentMessageRequest, agentErr error) {
	if agentErr != nil {
		log.Printf("[handler] default agent %q not available, using echo mode for %s: %v", req.name, req.userID, agentErr)
	}
	log.Printf("[handler] agent not ready, using echo mode for %s", req.userID)
	h.sendReplyWithMediaForRoute(req.ctx, req.reply, req.userID, req.routeUserID, req.name, "[echo] "+req.message)
}

// dispatchAgentMessage 根据 Agent 能力选择专用后台执行器或通用同步执行器。
func (h *Handler) dispatchAgentMessage(req agentMessageRequest, ag agent.Agent, prefix string) {
	runtime := agentDispatchRuntime{
		req: req, agent: ag, prefix: prefix,
		progressCfg: h.resolveProgressConfigForAccount(req.platformName, req.accountID, req.name),
	}
	if isCodexAgent(req.name, ag.Info()) {
		h.startCodexAgentTask(newCodexAgentTaskOptions(runtime))
		return
	}
	if isBackgroundClaudeAgent(req.name, ag) {
		h.startAgentTask(newAgentTaskOptions(runtime))
		return
	}
	h.runSynchronousAgentMessage(synchronousAgentRuntime{
		req: req, agent: ag, prefix: prefix, replyCtx: req.ctx, progressCfg: runtime.progressCfg,
	})
}

// newCodexAgentTaskOptions 将统一消息请求转换为 Codex 专用后台任务参数。
func newCodexAgentTaskOptions(runtime agentDispatchRuntime) codexAgentTaskOptions {
	return codexAgentTaskOptions{
		ctx: runtime.req.ctx, platform: runtime.req.platformName,
		userID: runtime.req.userID, routeUserID: runtime.req.routeUserID,
		reply: runtime.req.reply, agentName: runtime.req.name, message: runtime.req.message,
		clientID: runtime.req.clientID, replyPrefix: runtime.prefix,
		agent: runtime.agent, progressCfg: runtime.progressCfg,
	}
}

// newAgentTaskOptions 将统一消息请求转换为通用 ACP 后台任务参数。
func newAgentTaskOptions(runtime agentDispatchRuntime) agentTaskOptions {
	return agentTaskOptions{
		ctx: runtime.req.ctx, platformName: runtime.req.platformName, accountID: runtime.req.accountID,
		userID: runtime.req.userID, routeUserID: runtime.req.routeUserID, reply: runtime.req.reply,
		agentName: runtime.req.name, message: runtime.req.message, replyPrefix: runtime.prefix,
		agent: runtime.agent, progressCfg: runtime.progressCfg,
	}
}

// runSynchronousAgentMessage 为非后台 Agent 建立互斥锁和可停止任务状态。
func (h *Handler) runSynchronousAgentMessage(runtime synchronousAgentRuntime) {
	agentCtx, cancel := contextWithTaskTimeout(runtime.req.ctx, runtime.progressCfg)
	key := h.agentExecutionKeyForRoute(runtime.req.userID, runtime.req.routeUserID, runtime.req.name, runtime.agent)
	admission := h.beginOrQueueActiveTask(agentCtx, key, activeTaskMeta{
		owner: runtime.req.userID, routeUserID: runtime.req.routeUserID,
		agentName: runtime.req.name, message: runtime.req.message,
	}, h.pendingSynchronousAgentTask(runtime))
	if admission.status != activeTaskStarted {
		cancel()
		replyAgentTaskAdmission(agentTaskAdmissionNotice{
			ctx: runtime.replyCtx, reply: runtime.req.reply, userID: runtime.req.userID,
		}, admission.status)
		return
	}
	taskCtx := h.withAgentInteractions(admission.taskCtx, agentInteractionContextOptions{
		actorUserID: runtime.req.userID, routeUserID: runtime.req.routeUserID, reply: runtime.req.reply,
	})
	unlock := h.lockAgentExecution(key)
	runtime.lifecycle = h.startAgentTaskLifecycle(agentTaskLifecycleOptions{
		taskCtx: taskCtx, replyCtx: runtime.replyCtx, reply: runtime.req.reply,
		task: admission.task, cancel: cancel, executionKey: key,
		userID: runtime.req.userID, agentName: runtime.req.name, message: runtime.req.message,
		replyPrefix: runtime.prefix, progressConfig: runtime.progressCfg,
	})
	defer h.completeAgentTaskLifecycle(runtime.lifecycle)
	defer unlock()
	h.executeSynchronousAgentMessage(runtime)
}

// pendingSynchronousAgentTask 冻结同步任务请求，供当前任务结束后异步续跑。
func (h *Handler) pendingSynchronousAgentTask(runtime synchronousAgentRuntime) pendingAgentTask {
	frozenCtx := context.WithoutCancel(runtime.req.ctx)
	runtime.req.ctx = frozenCtx
	runtime.replyCtx = frozenCtx
	return pendingAgentTask{
		message: runtime.req.message,
		run:     func() { h.runSynchronousAgentMessage(runtime) },
	}
}

// executeSynchronousAgentMessage 解析会话并执行一次同步对话。
func (h *Handler) executeSynchronousAgentMessage(runtime synchronousAgentRuntime) {
	conversationID, err := h.resolveAgentConversationIDForRoute(
		runtime.lifecycle.opts.taskCtx, runtime.req.userID, runtime.req.routeUserID, runtime.req.name, runtime.agent,
	)
	if err != nil {
		h.finishSynchronousAgentMessage(synchronousAgentResult{runtime: runtime, err: err})
		return
	}
	reply, err := h.chatWithAgentWithProgress(
		runtime.lifecycle.opts.taskCtx, runtime.agent, conversationID,
		runtime.req.message, runtime.lifecycle.recordProgress,
	)
	if err == nil {
		h.recordCodexThread(runtime.req.routeUserID, runtime.req.name, runtime.agent, conversationID)
	}
	h.finishSynchronousAgentMessage(synchronousAgentResult{
		runtime: runtime, reply: reply, err: err,
	})
}

// finishSynchronousAgentMessage 统一收口同步任务的最终卡片和正文。
func (h *Handler) finishSynchronousAgentMessage(result synchronousAgentResult) {
	h.finishAgentTaskLifecycle(result.runtime.lifecycle, result.reply, result.err)
}
