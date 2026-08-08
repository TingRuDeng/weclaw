package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
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
	messageKey   string
	trace        observability.TraceContext
}

type broadcastAgentResult struct {
	ctx   context.Context
	trace observability.TraceContext
	name  string
	reply string
	skip  bool
}

func newBroadcastAgentResult(req broadcastAgentsRequest, name string, reply string, skip bool) broadcastAgentResult {
	return broadcastAgentResult{ctx: req.ctx, trace: req.trace, name: name, reply: reply, skip: skip}
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
	req.trace = req.trace.Branch(name)
	req.ctx = observability.ContextWithTrace(req.ctx, req.trace)
	h.recordTraceStage(req.trace, "agent.dispatched", "accepted", "agent="+name+" broadcast")
	ag, err := h.getAgent(req.ctx, name)
	if err != nil {
		results <- newBroadcastAgentResult(req, name, fmt.Sprintf("Error: %v", err), false)
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
	claudeBinding claudeTaskBindingSnapshot
	workspaceRoot string
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
		owner: req.userID, routeUserID: req.routeUserID, agentName: name, message: req.message, trace: req.trace,
	})
	if err != nil {
		unlock()
		results <- newBroadcastAgentResult(req, name, renderFinalFailure("["+name+"] ", err), false)
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
	binding, bindingErr := h.ensureClaudeSessions().requireWritableBinding(bindingKey)
	if bindingErr != nil {
		unlockBinding()
		results <- newBroadcastAgentResult(req, name, renderFinalFailure("["+name+"] ", errors.New(renderClaudeBindingError(bindingErr))), false)
		return broadcastAgentRuntime{}, false
	}
	bindingSnapshot := claudeTaskBindingSnapshot{SessionID: binding.SessionID, Revision: binding.Revision}
	key := h.agentExecutionKeyForRoute(req.userID, req.routeUserID, name, ag)
	pending := pendingAgentTask{message: req.message, run: func() {
		h.startAgentTask(agentTaskOptions{
			ctx: req.ctx, platformName: req.platformName, accountID: req.accountID,
			userID: req.userID, routeUserID: req.routeUserID, reply: reply,
			agentName: name, message: req.message, replyPrefix: "[" + name + "] ",
			agent: ag, progressCfg: h.resolveProgressConfigForAccount(req.platformName, req.accountID, name), trace: req.trace,
		})
	}}
	admission := h.beginOrQueueClaudeTask(ctx, key, activeTaskMeta{
		owner: req.userID, routeUserID: req.routeUserID, agentName: name, message: req.message,
		trace: req.trace.WithSession(binding.SessionID), sessionID: binding.SessionID,
	}, pending)
	if admission.status != activeTaskStarted {
		unlockBinding()
		h.replyBroadcastAdmission(req, name, admission.status, results)
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
		ctx: admission.taskCtx, executionKey: key, claudeBinding: bindingSnapshot,
		workspaceRoot: firstNonBlank(binding.WorkspaceRoot, h.claudeWorkspaceRootForUser(req.userID, name, ag)),
		activeTask:    admission.task, finishRuntime: finish,
	}, true
}

func (h *Handler) beginCodexBroadcastRuntime(req broadcastAgentsRequest, name string, ag agent.Agent, ctx context.Context, reply platform.Replier, results chan<- broadcastAgentResult) (broadcastAgentRuntime, bool) {
	bindingKey := codexBindingKey(req.routeUserID, name)
	unlockBinding := h.lockAgentExecution(codexBindingExecutionKey(bindingKey))
	defer unlockBinding()
	route := h.codexConversationRouteForSession(req.userID, req.routeUserID, name, ag)
	controlCtx, cancelControl := h.codexThreadControlContext(ctx)
	defer cancelControl()
	unlockControl, err := h.lockCodexThreadControlContext(controlCtx, route.threadID)
	if err != nil {
		results <- newBroadcastAgentResult(req, name, "Codex 会话控制繁忙，请稍后重试。", false)
		return broadcastAgentRuntime{}, false
	}
	defer unlockControl()
	taskOpts := codexAgentTaskOptions{
		ctx: ctx, platform: req.platformName, accountID: req.accountID,
		userID: req.userID, routeUserID: req.routeUserID, reply: reply,
		agentName: name, message: req.message, clientID: req.clientID, messageKey: req.messageKey,
		replyPrefix: "[" + name + "] ", agent: ag,
		progressCfg: h.resolveProgressConfigForAccount(req.platformName, req.accountID, name), route: route,
		trace: req.trace.WithConversation(route.conversationID).WithThreadTurn(route.threadID, ""),
	}
	if h.preflightCodexTaskStart(codexTaskPreflightOptions{
		taskOpts: taskOpts, route: route, cancel: func() {},
	}) {
		results <- newBroadcastAgentResult(req, name, "", true)
		return broadcastAgentRuntime{}, false
	}
	key := route.conversationID
	pending := h.broadcastPendingCodexTask(req, name, ag, route, reply)
	admission := h.beginOrQueueActiveTask(ctx, key, activeTaskMeta{
		owner: req.userID, routeUserID: req.routeUserID, agentName: name, message: req.message,
		codexThreadID: route.threadID, inProcessCodexLifecycle: true, trace: taskOpts.trace,
	}, pending)
	if admission.status != activeTaskStarted {
		h.replyBroadcastAdmission(req, name, admission.status, results)
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
	return broadcastAgentRuntime{
		ctx: taskCtx, executionKey: key, codexRoute: route, workspaceRoot: route.workspaceRoot,
		activeTask: task, finishRuntime: finish,
	}, true
}

func (h *Handler) broadcastPendingCodexTask(req broadcastAgentsRequest, name string, ag agent.Agent, route codexConversationRoute, reply platform.Replier) pendingAgentTask {
	return h.pendingCodexTask(codexAgentTaskOptions{
		ctx: req.ctx, platform: req.platformName, accountID: req.accountID,
		userID: req.userID, routeUserID: req.routeUserID, reply: reply,
		agentName: name, message: req.message, clientID: req.clientID, messageKey: req.messageKey,
		replyPrefix: "[" + name + "] ", agent: ag,
		progressCfg: h.resolveProgressConfigForAccount(req.platformName, req.accountID, name), route: route,
		trace: req.trace.WithConversation(route.conversationID).WithThreadTurn(route.threadID, ""),
	})
}

func (h *Handler) replyBroadcastAdmission(req broadcastAgentsRequest, name string, status activeTaskAdmissionStatus, results chan<- broadcastAgentResult) {
	text := "当前任务已有一条暂存消息，请先处理后再发送。"
	if status == activeTaskQueued {
		text = queuedAgentMessage
	} else if status == activeTaskForeignWriter {
		text = "当前 Claude session 正由另一个窗口执行任务，请等待该任务结束后重试。"
	} else if status == activeTaskDraining {
		text = "WeClaw 正在安全重启，暂不接收新任务，请稍后重试。"
	}
	results <- newBroadcastAgentResult(req, name, text, false)
}

func (h *Handler) executeBroadcastAgent(req broadcastAgentsRequest, name string, ag agent.Agent, runtime broadcastAgentRuntime, reply platform.Replier, progressCfg config.ProgressConfig, results chan<- broadcastAgentResult) {
	if runtime.claudeBinding.SessionID != "" {
		bindingKey := claudeBindingKey(req.routeUserID, name)
		if err := h.ensureClaudeSessions().validateBindingSnapshot(bindingKey, runtime.claudeBinding); err != nil {
			results <- newBroadcastAgentResult(req, name, renderFinalFailure("["+name+"] ", errors.New(renderClaudeBindingError(err))), false)
			return
		}
	}
	onProgress, finishProgress, progressSession := h.startProgressSessionForWorkspaceAgentWithHandle(
		runtime.ctx, reply, "["+name+"] ", name, runtime.workspaceRoot, req.message, progressCfg,
	)
	if runtime.activeTask != nil {
		runtime.activeTask.attachProgressSession(progressSession)
		defer runtime.activeTask.detachProgressSession(progressSession)
	}
	trace := traceWithReply(req.trace, reply)
	if runtime.activeTask != nil {
		runtime.activeTask.mu.Lock()
		runtime.activeTask.trace = traceWithReply(runtime.activeTask.trace, reply)
		runtime.activeTask.mu.Unlock()
		trace = runtime.activeTask.traceSnapshot()
	}
	h.recordTraceStage(trace, "task.started", "running", "agent="+name+" broadcast")
	onProgressEvent := func(event agent.ProgressEvent) {
		text := event.DisplayText()
		update := taskProgressUpdate{latest: text, card: text}
		if runtime.activeTask != nil {
			var recorded bool
			update, recorded = runtime.activeTask.recordProgressUpdate(time.Now(), event)
			if !recorded || !runtime.activeTask.shouldSendFinal() {
				return
			}
			text = update.latest
		}
		if text != "" {
			progressTrace := trace
			if runtime.activeTask != nil {
				progressTrace = runtime.activeTask.traceSnapshot()
			}
			h.recordProgressTrace(progressTrace, event, text)
			if progressSession != nil {
				progressSession.onTaskProgress(update)
			} else {
				onProgress(text)
			}
		}
	}
	send := func(text string, failed bool, stopped bool) {
		if runtime.activeTask != nil {
			runtime.activeTask.closeProgress()
			terminalState := "completed"
			if stopped {
				terminalState = "stopped"
			} else if failed {
				terminalState = "failed"
			}
			runtime.activeTask.recordTerminalView(time.Now(), terminalState)
			trace = runtime.activeTask.traceSnapshot()
		}
		state, stage := "completed", "task.completed"
		if stopped {
			state, stage = "stopped", "task.stopped"
		} else if failed {
			state, stage = "failed", "task.failed"
		}
		h.recordTraceStage(trace, stage, state, "broadcast agent terminal")
		if stopped && runtime.activeTask != nil && !runtime.activeTask.shouldSendFinal() {
			h.recordTraceStage(trace, "task.delivery_suppressed", "detached", "final reply suppressed")
			if progressSession != nil {
				_ = progressSession.stopWithTerminal("", false, true)
			} else {
				_ = finishProgress("", false)
			}
			results <- newBroadcastAgentResult(req, name, "", true)
			return
		}
		h.finishAndSendProgressReply(progressReplyDelivery{
			delivery: replyDeliveryRequest{
				ctx: req.ctx, replyWriter: reply, userID: req.userID,
				agentName: name, reply: text, trace: trace,
			},
			failed: failed, stopped: stopped,
			finish: finishProgress, progress: progressSession,
		})
		results <- newBroadcastAgentResult(req, name, "", true)
	}
	conversationID, err := h.broadcastConversationID(runtime.ctx, req, name, ag, runtime.codexRoute)
	if err != nil {
		send(renderFinalFailure("["+name+"] ", err), true, false)
		return
	}
	if runtime.activeTask != nil {
		runtime.activeTask.setTraceConversation(conversationID, runtime.claudeBinding.SessionID)
		trace = runtime.activeTask.traceSnapshot()
	}
	text, err := h.executeBroadcastAgentTurn(broadcastAgentTurnOptions{
		request: req, name: name, agent: ag, runtime: runtime,
		conversationID: conversationID, onProgress: onProgressEvent,
	})
	if err != nil {
		if runtime.activeTask != nil && runtime.activeTask.stoppedByRequest(err) {
			send(renderFinalStopped("["+name+"] "), false, true)
			return
		}
		send(renderFinalFailure("["+name+"] ", err), true, false)
		return
	}
	h.recordBroadcastSession(req, name, ag, conversationID, runtime.codexRoute)
	if runtime.activeTask != nil && !runtime.activeTask.shouldSendFinal() {
		_ = finishProgress("", false)
		results <- newBroadcastAgentResult(req, name, "", true)
		return
	}
	send(renderFinalSuccess("["+name+"] ", text), false, false)
}

type broadcastAgentTurnOptions struct {
	request        broadcastAgentsRequest
	name           string
	agent          agent.Agent
	runtime        broadcastAgentRuntime
	conversationID string
	onProgress     func(agent.ProgressEvent)
}

// executeBroadcastAgentTurn 让广播中的 Codex 同样经过控制权和 writer lease。
func (h *Handler) executeBroadcastAgentTurn(opts broadcastAgentTurnOptions) (string, error) {
	if !isCodexAgent(opts.name, opts.agent.Info()) {
		return h.chatWithAgentWithProgressEvents(
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
	ctx := result.ctx
	if ctx == nil {
		ctx = req.ctx
	}
	trace := result.trace
	if trace.TraceID != "" {
		ctx = observability.ContextWithTrace(ctx, traceWithReply(trace, reply))
	}
	h.sendReplyWithMediaAfterStreamForRoute(ctx, reply, req.userID, req.routeUserID, result.name, result.reply, false)
}
