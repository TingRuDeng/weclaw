package messaging

import (
	"context"
	"fmt"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

type broadcastAgentsRequest struct {
	ctx          context.Context
	platformName platform.PlatformName
	accountID    string
	userID       string
	routeUserID  string
	replyWriter  platform.Replier
	names        []string
	message      string
	clientID     string
}

type broadcastAgentResult struct {
	name          string
	reply         string
	skip          bool
	finalInStream bool
}

// broadcastToAgents 并行执行多个 Agent，并按完成顺序发送各自结果。
func (h *Handler) broadcastToAgents(req broadcastAgentsRequest) {
	results := make(chan broadcastAgentResult, len(req.names))
	reply := newSerializedReplier(req.replyWriter)
	for _, name := range req.names {
		go h.runBroadcastAgent(req, reply, name, results)
	}
	for range req.names {
		h.sendBroadcastAgentResult(req, reply, <-results)
	}
}

func (h *Handler) runBroadcastAgent(req broadcastAgentsRequest, reply platform.Replier, name string, results chan<- broadcastAgentResult) {
	ag, err := h.getAgent(req.ctx, name)
	if err != nil {
		results <- broadcastAgentResult{name: name, reply: fmt.Sprintf("Error: %v", err)}
		return
	}
	progressCfg := h.resolveProgressConfigForAccount(req.platformName, req.accountID, name)
	agentCtx, cancel := contextWithTaskTimeout(req.ctx, progressCfg)
	defer cancel()
	agentCtx = agent.ContextWithApprovalHandler(agentCtx, h.approvalHandlerForUser(req.userID, req.routeUserID, reply))
	runtime, ok := h.beginBroadcastRuntime(req, name, ag, agentCtx, reply, results)
	if !ok {
		return
	}
	defer runtime.finish()
	h.executeBroadcastAgent(req, name, ag, runtime, reply, progressCfg, results)
}

type broadcastAgentRuntime struct {
	ctx           context.Context
	executionKey  string
	codexRoute    codexConversationRoute
	activeTask    *activeAgentTask
	finishRuntime func()
}

func (r broadcastAgentRuntime) finish() {
	if r.finishRuntime != nil {
		r.finishRuntime()
	}
}

func (h *Handler) beginBroadcastRuntime(req broadcastAgentsRequest, name string, ag agent.Agent, ctx context.Context, reply platform.Replier, results chan<- broadcastAgentResult) (broadcastAgentRuntime, bool) {
	if isCodexAgent(name, ag.Info()) {
		return h.beginCodexBroadcastRuntime(req, name, ag, ctx, reply, results)
	}
	key := h.agentExecutionKeyForRoute(req.userID, req.routeUserID, name, ag)
	unlock := h.lockAgentExecution(key)
	task, taskCtx, err := h.beginSynchronousActiveTask(ctx, key, activeTaskMeta{owner: req.userID, agentName: name, message: req.message})
	if err != nil {
		unlock()
		results <- broadcastAgentResult{name: name, reply: renderFinalFailure("["+name+"] ", err)}
		return broadcastAgentRuntime{}, false
	}
	finish := func() {
		h.finishActiveTask(key, task)
		unlock()
	}
	return broadcastAgentRuntime{ctx: taskCtx, executionKey: key, activeTask: task, finishRuntime: finish}, true
}

func (h *Handler) beginCodexBroadcastRuntime(req broadcastAgentsRequest, name string, ag agent.Agent, ctx context.Context, reply platform.Replier, results chan<- broadcastAgentResult) (broadcastAgentRuntime, bool) {
	route := h.codexConversationRouteForSession(req.userID, req.routeUserID, name, ag)
	key := route.conversationID
	task, taskCtx, started := h.beginActiveTask(ctx, key, activeTaskMeta{owner: req.userID, agentName: name, message: req.message})
	if !started {
		h.deferBroadcastCodexMessage(req, name, ag, route, reply, key, task, results)
		return broadcastAgentRuntime{}, false
	}
	unlock := h.lockAgentExecution(key)
	finish := func() {
		unlock()
		if pending, ok := h.completeActiveTask(key, task); ok {
			pending.run()
		}
	}
	return broadcastAgentRuntime{ctx: taskCtx, executionKey: key, codexRoute: route, activeTask: task, finishRuntime: finish}, true
}

func (h *Handler) deferBroadcastCodexMessage(req broadcastAgentsRequest, name string, ag agent.Agent, route codexConversationRoute, reply platform.Replier, key string, task *activeAgentTask, results chan<- broadcastAgentResult) {
	pending := h.pendingCodexTask(codexAgentTaskOptions{
		ctx: req.ctx, userID: req.userID, routeUserID: req.routeUserID, reply: reply,
		agentName: name, message: req.message, clientID: req.clientID,
		replyPrefix: "[" + name + "] ", agent: ag,
		progressCfg: h.resolveProgressConfigForAccount(req.platformName, req.accountID, name), route: route,
	})
	text := "当前任务已有一条暂存消息，请先处理后再发送。"
	if h.storePendingGuide(key, pending) {
		text = runningCodexGuidePromptForTask(task)
	}
	results <- broadcastAgentResult{name: name, reply: text}
}

func (h *Handler) executeBroadcastAgent(req broadcastAgentsRequest, name string, ag agent.Agent, runtime broadcastAgentRuntime, reply platform.Replier, progressCfg config.ProgressConfig, results chan<- broadcastAgentResult) {
	onProgress, finishProgress := h.startProgressSessionWithFinal(runtime.ctx, reply, "["+name+"] ", req.message, progressCfg)
	send := func(text string, failed bool) {
		consumed := finishProgressWithReplyForPlatform(reply, finishProgress, text, failed)
		results <- broadcastAgentResult{name: name, reply: text, finalInStream: consumed}
	}
	conversationID, err := h.broadcastConversationID(runtime.ctx, req, name, ag, runtime.codexRoute)
	if err != nil {
		send(renderFinalFailure("["+name+"] ", err), true)
		return
	}
	text, err := h.chatWithAgentWithProgress(runtime.ctx, ag, conversationID, req.message, onProgress)
	if err != nil {
		send(renderFinalFailure("["+name+"] ", err), true)
		return
	}
	h.recordBroadcastSession(req, name, ag, conversationID, runtime.codexRoute)
	if runtime.activeTask != nil && !runtime.activeTask.shouldSendFinal() {
		_ = finishProgress("", false)
		results <- broadcastAgentResult{name: name, skip: true}
		return
	}
	send(renderFinalSuccess("["+name+"] ", text), false)
}

func (h *Handler) broadcastConversationID(ctx context.Context, req broadcastAgentsRequest, name string, ag agent.Agent, route codexConversationRoute) (string, error) {
	if isCodexAgent(name, ag.Info()) {
		if err := h.prepareCodexConversation(ctx, route, ag); err != nil {
			return "", err
		}
		return route.conversationID, nil
	}
	return h.resolveAgentConversationIDForRoute(ctx, req.userID, req.routeUserID, name, ag)
}

func (h *Handler) recordBroadcastSession(req broadcastAgentsRequest, name string, ag agent.Agent, conversationID string, route codexConversationRoute) {
	if isCodexAgent(name, ag.Info()) {
		h.recordCodexThreadForWorkspace(req.routeUserID, name, ag, conversationID, route.workspaceRoot)
		return
	}
	h.recordCodexThread(req.routeUserID, name, ag, conversationID)
	h.recordClaudeSessionForRoute(req.userID, req.routeUserID, name, ag, conversationID)
}

func (h *Handler) sendBroadcastAgentResult(req broadcastAgentsRequest, reply platform.Replier, result broadcastAgentResult) {
	if result.skip {
		return
	}
	if wxReply, ok := req.replyWriter.(*wechat.Replier); ok {
		wxReply.ClientID = NewClientID()
	}
	h.sendReplyWithMediaAfterStreamForRoute(req.ctx, reply, req.userID, req.routeUserID, result.name, result.reply, result.finalInStream)
}
