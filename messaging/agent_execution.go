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
	agentCtx    context.Context
	replyCtx    context.Context
	progressCfg config.ProgressConfig
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
	finish  func(string, bool) bool
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
		ctx: runtime.req.ctx, userID: runtime.req.userID, routeUserID: runtime.req.routeUserID,
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
	defer cancel()
	runtime.agentCtx = h.withAgentInteractions(agentCtx, agentInteractionContextOptions{
		actorUserID: runtime.req.userID, routeUserID: runtime.req.routeUserID, reply: runtime.req.reply,
	})
	key := h.agentExecutionKeyForRoute(runtime.req.userID, runtime.req.routeUserID, runtime.req.name, runtime.agent)
	unlock := h.lockAgentExecution(key)
	defer unlock()
	task, trackedCtx, err := h.beginSynchronousActiveTask(runtime.agentCtx, key, activeTaskMeta{
		owner: runtime.req.userID, routeUserID: runtime.req.routeUserID,
		agentName: runtime.req.name, message: runtime.req.message,
	})
	if err != nil {
		h.sendSynchronousStartFailure(runtime, err)
		return
	}
	runtime.agentCtx = trackedCtx
	defer h.finishActiveTask(key, task)
	h.executeSynchronousAgentMessage(runtime)
}

// sendSynchronousStartFailure 统一发送同步任务登记失败结果。
func (h *Handler) sendSynchronousStartFailure(runtime synchronousAgentRuntime, err error) {
	reply := renderFinalFailure(runtime.prefix, err)
	h.sendReplyWithMediaForRoute(
		runtime.replyCtx, runtime.req.reply, runtime.req.userID,
		runtime.req.routeUserID, runtime.req.name, reply,
	)
}

// executeSynchronousAgentMessage 解析会话并执行一次同步对话。
func (h *Handler) executeSynchronousAgentMessage(runtime synchronousAgentRuntime) {
	onProgress, finish := h.startProgressSessionWithFinal(
		runtime.agentCtx, runtime.req.reply, "", runtime.req.message, runtime.progressCfg,
	)
	conversationID, err := h.resolveAgentConversationIDForRoute(
		runtime.agentCtx, runtime.req.userID, runtime.req.routeUserID, runtime.req.name, runtime.agent,
	)
	if err != nil {
		h.finishSynchronousAgentMessage(synchronousAgentResult{runtime: runtime, err: err, finish: finish})
		return
	}
	reply, err := h.chatWithAgentWithProgress(
		runtime.agentCtx, runtime.agent, conversationID, runtime.req.message, onProgress,
	)
	if err == nil {
		h.recordCodexThread(runtime.req.routeUserID, runtime.req.name, runtime.agent, conversationID)
	}
	h.finishSynchronousAgentMessage(synchronousAgentResult{
		runtime: runtime, reply: reply, err: err, finish: finish,
	})
}

// finishSynchronousAgentMessage 统一收口同步任务的最终卡片和正文。
func (h *Handler) finishSynchronousAgentMessage(result synchronousAgentResult) {
	if result.err != nil {
		result.reply = renderFinalFailure(result.runtime.prefix, result.err)
	} else {
		result.reply = renderFinalSuccess(result.runtime.prefix, result.reply)
	}
	h.finishAndSendProgressReply(progressReplyDelivery{
		delivery: replyDeliveryRequest{
			ctx: result.runtime.replyCtx, replyWriter: result.runtime.req.reply,
			userID: result.runtime.req.userID, agentName: result.runtime.req.name, reply: result.reply,
		},
		failed: result.err != nil, finish: result.finish,
	})
}
