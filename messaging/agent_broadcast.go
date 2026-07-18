package messaging

import (
	"context"
	"errors"
	"fmt"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
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
	name  string
	reply string
	skip  bool
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
	agentCtx = h.withAgentInteractions(agentCtx, agentInteractionContextOptions{
		actorUserID: req.userID, routeUserID: req.routeUserID,
		agentName: name, reply: reply,
	})
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
	claudeControl claudeTaskControlSnapshot
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
	if isBackgroundClaudeAgent(name, ag) {
		return h.beginClaudeBroadcastRuntime(req, name, ag, ctx, reply, results)
	}
	key := h.agentExecutionKeyForRoute(req.userID, req.routeUserID, name, ag)
	unlock := h.lockAgentExecution(key)
	task, taskCtx, err := h.beginSynchronousActiveTask(ctx, key, activeTaskMeta{
		owner: req.userID, routeUserID: req.routeUserID, agentName: name, message: req.message,
	})
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

// beginClaudeBroadcastRuntime 让 Claude ACP 广播与普通消息共享原子任务准入和单条暂存队列。
func (h *Handler) beginClaudeBroadcastRuntime(req broadcastAgentsRequest, name string, ag agent.Agent, ctx context.Context, reply platform.Replier, results chan<- broadcastAgentResult) (broadcastAgentRuntime, bool) {
	bindingKey := claudeBindingKey(req.routeUserID, name)
	unlockBinding := h.lockAgentExecution(claudeBindingExecutionKey(bindingKey))
	binding, intent, controlErr := h.ensureClaudeSessions().requireRemoteControl(bindingKey)
	if controlErr != nil {
		unlockBinding()
		results <- broadcastAgentResult{name: name, reply: renderFinalFailure("["+name+"] ", errors.New(renderClaudeRemoteControlError(controlErr)))}
		return broadcastAgentRuntime{}, false
	}
	control := claudeTaskControlSnapshot{SessionID: binding.SessionID, Revision: intent.Revision}
	key := h.agentExecutionKeyForRoute(req.userID, req.routeUserID, name, ag)
	pending := pendingAgentTask{message: req.message, run: func() {
		h.startAgentTask(agentTaskOptions{
			ctx: req.ctx, platformName: req.platformName, accountID: req.accountID,
			userID: req.userID, routeUserID: req.routeUserID, reply: reply,
			agentName: name, message: req.message, replyPrefix: "[" + name + "] ",
			agent: ag, progressCfg: h.resolveProgressConfigForAccount(req.platformName, req.accountID, name),
		})
	}}
	admission := h.beginOrQueueActiveTask(ctx, key, activeTaskMeta{
		owner: req.userID, routeUserID: req.routeUserID, agentName: name, message: req.message,
	}, pending)
	if admission.status != activeTaskStarted {
		unlockBinding()
		h.replyBroadcastAdmission(name, admission.status, results)
		return broadcastAgentRuntime{}, false
	}
	unlockBinding()
	unlock := h.lockAgentExecution(key)
	finish := func() {
		unlock()
		if next, ok := h.completeActiveTask(key, admission.task); ok {
			next.run()
		}
	}
	return broadcastAgentRuntime{
		ctx: admission.taskCtx, executionKey: key, claudeControl: control,
		activeTask: admission.task, finishRuntime: finish,
	}, true
}

func (h *Handler) beginCodexBroadcastRuntime(req broadcastAgentsRequest, name string, ag agent.Agent, ctx context.Context, reply platform.Replier, results chan<- broadcastAgentResult) (broadcastAgentRuntime, bool) {
	bindingKey := codexBindingKey(req.routeUserID, name)
	unlockBinding := h.lockAgentExecution(codexBindingExecutionKey(bindingKey))
	defer unlockBinding()
	route := h.codexConversationRouteForSession(req.userID, req.routeUserID, name, ag)
	unlockControl := h.lockCodexThreadControl(route.threadID)
	defer unlockControl()
	taskOpts := codexAgentTaskOptions{
		ctx: ctx, platform: req.platformName,
		userID: req.userID, routeUserID: req.routeUserID, reply: reply,
		agentName: name, message: req.message, clientID: req.clientID,
		replyPrefix: "[" + name + "] ", agent: ag,
		progressCfg: h.resolveProgressConfigForAccount(req.platformName, req.accountID, name), route: route,
	}
	if h.preflightCodexTaskStart(codexTaskPreflightOptions{
		taskOpts: taskOpts, route: route, cancel: func() {},
	}) {
		results <- broadcastAgentResult{name: name, skip: true}
		return broadcastAgentRuntime{}, false
	}
	key := route.conversationID
	pending := h.broadcastPendingCodexTask(req, name, ag, route, reply)
	admission := h.beginOrQueueActiveTask(ctx, key, activeTaskMeta{
		owner: req.userID, routeUserID: req.routeUserID, agentName: name, message: req.message,
		codexThreadID: route.threadID, inProcessCodexLifecycle: true,
	}, pending)
	if admission.status != activeTaskStarted {
		h.replyBroadcastAdmission(name, admission.status, results)
		return broadcastAgentRuntime{}, false
	}
	task := admission.task
	taskCtx := admission.taskCtx
	unlock := h.lockAgentExecution(key)
	finish := func() {
		unlock()
		if pending, ok := h.completeActiveTask(key, task); ok {
			pending.run()
		}
	}
	return broadcastAgentRuntime{ctx: taskCtx, executionKey: key, codexRoute: route, activeTask: task, finishRuntime: finish}, true
}

func (h *Handler) broadcastPendingCodexTask(req broadcastAgentsRequest, name string, ag agent.Agent, route codexConversationRoute, reply platform.Replier) pendingAgentTask {
	return h.pendingCodexTask(codexAgentTaskOptions{
		ctx: req.ctx, platform: req.platformName,
		userID: req.userID, routeUserID: req.routeUserID, reply: reply,
		agentName: name, message: req.message, clientID: req.clientID,
		replyPrefix: "[" + name + "] ", agent: ag,
		progressCfg: h.resolveProgressConfigForAccount(req.platformName, req.accountID, name), route: route,
	})
}

func (h *Handler) replyBroadcastAdmission(name string, status activeTaskAdmissionStatus, results chan<- broadcastAgentResult) {
	text := "当前任务已有一条暂存消息，请先处理后再发送。"
	if status == activeTaskQueued {
		text = queuedAgentMessage
	}
	results <- broadcastAgentResult{name: name, reply: text}
}

func (h *Handler) executeBroadcastAgent(req broadcastAgentsRequest, name string, ag agent.Agent, runtime broadcastAgentRuntime, reply platform.Replier, progressCfg config.ProgressConfig, results chan<- broadcastAgentResult) {
	if runtime.claudeControl.SessionID != "" {
		bindingKey := claudeBindingKey(req.routeUserID, name)
		if err := h.ensureClaudeSessions().validateRemoteControlSnapshot(bindingKey, runtime.claudeControl); err != nil {
			results <- broadcastAgentResult{
				name: name, reply: renderFinalFailure("["+name+"] ", errors.New(renderClaudeRemoteControlError(err))),
			}
			return
		}
	}
	onProgress, finishProgress := h.startProgressSessionForAgentWithFinal(runtime.ctx, reply, "["+name+"] ", name, req.message, progressCfg)
	send := func(text string, failed bool) {
		h.finishAndSendProgressReply(progressReplyDelivery{
			delivery: replyDeliveryRequest{
				ctx: req.ctx, replyWriter: reply, userID: req.userID,
				agentName: name, reply: text,
			},
			failed: failed, finish: finishProgress,
		})
		results <- broadcastAgentResult{name: name, skip: true}
	}
	conversationID, err := h.broadcastConversationID(runtime.ctx, req, name, ag, runtime.codexRoute)
	if err != nil {
		send(renderFinalFailure("["+name+"] ", err), true)
		return
	}
	text, err := h.executeBroadcastAgentTurn(broadcastAgentTurnOptions{
		request: req, name: name, agent: ag, runtime: runtime,
		conversationID: conversationID, onProgress: onProgress,
	})
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

type broadcastAgentTurnOptions struct {
	request        broadcastAgentsRequest
	name           string
	agent          agent.Agent
	runtime        broadcastAgentRuntime
	conversationID string
	onProgress     func(string)
}

// executeBroadcastAgentTurn 让广播中的 Codex 同样经过控制权和 writer lease。
func (h *Handler) executeBroadcastAgentTurn(opts broadcastAgentTurnOptions) (string, error) {
	if !isCodexAgent(opts.name, opts.agent.Info()) {
		return h.chatWithAgentWithProgress(
			opts.runtime.ctx, opts.agent, opts.conversationID, opts.request.message, opts.onProgress,
		)
	}
	reply, err := h.runControlledCodexTurn(codexControlledTurnOptions{
		ctx: opts.runtime.ctx, agent: opts.agent, route: opts.runtime.codexRoute,
		message: opts.request.message, onProgress: opts.onProgress, task: opts.runtime.activeTask,
	})
	var interrupted *agent.CodexTurnInterruptedError
	if !errors.As(err, &interrupted) {
		return reply, err
	}
	if opts.runtime.activeTask != nil {
		opts.runtime.activeTask.markCodexObservationInterrupted(interrupted.ThreadID, interrupted.TurnID)
	}
	result := h.reconcileInterruptedCodexTurn(opts.runtime.ctx, interrupted, opts.onProgress)
	confirmInterruptedCodexTerminal(interrupted, result)
	if result.Terminal && !result.Failed {
		return result.Final, nil
	}
	return "", firstNonNilError(result.Err, interrupted)
}

// firstNonNilError 保留核对阶段的具体错误，否则返回原始中断。
func firstNonNilError(primary error, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
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
}

func (h *Handler) sendBroadcastAgentResult(req broadcastAgentsRequest, reply platform.Replier, result broadcastAgentResult) {
	if result.skip {
		return
	}
	h.sendReplyWithMediaAfterStreamForRoute(req.ctx, reply, req.userID, req.routeUserID, result.name, result.reply, false)
}
