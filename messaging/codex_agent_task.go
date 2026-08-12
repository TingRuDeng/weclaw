package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// startCodexAgentTask 先登记 active task 再后台执行，保证 /guide 和 /cancel 可及时进入 Handler。
func (h *Handler) startCodexAgentTask(opts codexAgentTaskOptions) {
	if strings.TrimSpace(opts.routeUserID) == "" {
		opts.routeUserID = opts.userID
	}
	bindingKey := codexBindingKey(opts.routeUserID, opts.agentName)
	unlockRegistry := h.lockWorkspaceRegistryControl()
	defer unlockRegistry()
	unlockBinding := h.lockAgentExecution(codexBindingExecutionKey(bindingKey))
	defer unlockBinding()
	// 后台任务保留消息上下文值，但不能随平台请求返回而被取消。
	opts.ctx = context.WithoutCancel(opts.ctx)
	agentCtx, cancelTaskTimeout := contextWithTaskTimeout(opts.ctx, opts.progressCfg)
	interactionLease := &agentInteractionLease{}
	_, liveCodexLifecycle := opts.agent.(agent.CodexLiveRuntimeAgent)
	var detachCodexObserver func()
	if liveCodexLifecycle {
		agentCtx, detachCodexObserver = agent.ContextWithCodexObserverDetach(agentCtx)
	}
	agentCtx = h.withAgentInteractions(agentCtx, agentInteractionContextOptions{
		actorUserID: opts.userID, routeUserID: opts.routeUserID,
		agentName: opts.agentName, reply: opts.reply, lease: interactionLease,
	})
	route := opts.route
	if route.conversationID == "" {
		route = h.codexConversationRouteForSession(opts.userID, opts.routeUserID, opts.agentName, opts.agent)
	}
	if err := h.hiddenWorkspaceError(opts.agentName, route.workspaceRoot, "cx"); err != nil {
		sendPlatformText(opts.ctx, opts.reply, opts.userID, err.Error())
		cancelTaskTimeout()
		return
	}
	if !h.workspaceAllowedForAgentContext(opts.ctx, opts.agentName, route.workspaceRoot) {
		sendPlatformText(opts.ctx, opts.reply, opts.userID, "当前工作空间不在允许范围，请发送 /cx ls 重新选择。")
		cancelTaskTimeout()
		return
	}
	controlCtx, cancelControl := h.codexThreadControlContext(agentCtx)
	defer cancelControl()
	unlockControl, err := h.lockCodexThreadControlContext(controlCtx, route.threadID)
	if err != nil {
		cancelTaskTimeout()
		sendPlatformText(opts.ctx, opts.reply, opts.userID, "当前 Codex 会话控制繁忙，请稍后重试。")
		return
	}
	defer unlockControl()
	if h.preflightCodexTaskStart(codexTaskPreflightOptions{
		taskOpts: opts, route: route, cancel: cancelTaskTimeout,
	}) {
		return
	}
	executionKey := route.conversationID
	runtimeOwner, ownerRevision := codexTaskOwnerSnapshot(opts.agent)
	opts.route = route
	admission := h.beginOrQueueActiveTask(agentCtx, executionKey, activeTaskMeta{
		owner:        opts.userID,
		routeUserID:  opts.routeUserID,
		agentName:    opts.agentName,
		message:      opts.message,
		runtimeOwner: runtimeOwner, ownerRevision: ownerRevision,
		codexThreadID: route.threadID, inProcessCodexLifecycle: liveCodexLifecycle,
		interactionLease: interactionLease, detachCodexObserver: detachCodexObserver,
		trace: opts.trace.WithConversation(route.conversationID).WithThreadTurn(route.threadID, ""),
	}, h.pendingCodexTask(opts))
	if admission.status != activeTaskStarted {
		h.recordTaskAdmissionTrace(opts.trace, admission.status)
		cancelTaskTimeout()
		h.replyAgentTaskAdmission(agentTaskAdmissionNotice{
			ctx: opts.ctx, platformName: opts.platform, accountID: opts.accountID,
			reply: opts.reply, userID: opts.userID, routeUserID: opts.routeUserID,
			agentName: opts.agentName, executionKey: executionKey, task: admission.task, guideSupported: true,
		}, admission.status)
		return
	}
	task := admission.task
	taskCtx := admission.taskCtx

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
		run: func() {
			opts.route = h.refreshReplacedCodexRoute(opts.route)
			h.startCodexAgentTask(opts)
		},
	}
}

// refreshReplacedCodexRoute follows a first-turn replacement recorded in this
// frontend's binding. No global owner transition is involved.
func (h *Handler) refreshReplacedCodexRoute(route codexConversationRoute) codexConversationRoute {
	threadID, pending := h.ensureCodexSessions().getThread(route.bindingKey, route.workspaceRoot)
	threadID = strings.TrimSpace(threadID)
	if pending || threadID == "" || threadID == route.threadID {
		return route
	}
	route.threadID = threadID
	return route
}

// runCodexAgentTask 在后台完成 Codex 调用和最终回复发送。
func (h *Handler) runCodexAgentTask(runtime codexAgentTaskRuntime) {
	opts := runtime.opts
	unlock := h.lockAgentExecution(runtime.executionKey)
	lifecycle := h.startAgentTaskLifecycle(agentTaskLifecycleOptions{
		taskCtx: runtime.agentCtx, replyCtx: opts.ctx, reply: opts.reply,
		task: runtime.task, cancel: runtime.cancelTaskTimeout, executionKey: runtime.executionKey,
		userID: opts.userID, agentName: opts.agentName, workspaceRoot: runtime.route.workspaceRoot, message: opts.message,
		replyPrefix: opts.replyPrefix, progressConfig: opts.progressCfg, trace: runtime.task.traceSnapshot(),
	})
	defer h.completeAgentTaskLifecycle(lifecycle)
	defer unlock()

	if err := h.prepareCodexConversation(runtime.agentCtx, runtime.route, opts.agent); err != nil {
		h.finishAgentTaskLifecycle(lifecycle, "", err)
		return
	}
	reply, err := h.executeCodexAgentTurn(runtime, lifecycle.recordProgress)
	if err == nil {
		h.recordCodexThreadForWorkspace(opts.routeUserID, opts.agentName, opts.agent, runtime.route.conversationID, runtime.route.workspaceRoot)
	}
	h.finishAgentTaskLifecycle(lifecycle, reply, err)
}

// executeCodexAgentTurn 在观察流中断时接续同一 rollout turn，不重复执行任务。
func (h *Handler) executeCodexAgentTurn(runtime codexAgentTaskRuntime, onProgress func(agent.ProgressEvent)) (string, error) {
	reply, err := h.runCodexAgentTurn(runtime, onProgress)
	var interrupted *agent.CodexTurnInterruptedError
	if !errors.As(err, &interrupted) {
		return reply, err
	}
	runtime.task.markCodexObservationInterrupted(interrupted.ThreadID, interrupted.TurnID)
	result := h.reconcileInterruptedCodexTurn(runtime.agentCtx, interrupted, onProgress)
	confirmInterruptedCodexTerminal(interrupted, result)
	if result.Terminal && !result.Failed {
		return result.Final, nil
	}
	if result.Err != nil {
		return "", result.Err
	}
	return "", interrupted
}

func confirmInterruptedCodexTerminal(interrupted *agent.CodexTurnInterruptedError, result codexExternalWatchResult) {
	if interrupted != nil && result.ConfirmedTerminal {
		interrupted.ConfirmTerminal()
	}
}

// runCodexAgentTurn 让新版 Codex 在 writer lease 内执行，旧 Agent 保持原调用路径。
func (h *Handler) runCodexAgentTurn(runtime codexAgentTaskRuntime, onProgress func(agent.ProgressEvent)) (string, error) {
	return h.runControlledCodexTurn(codexControlledTurnOptions{
		ctx: runtime.agentCtx, agent: runtime.opts.agent, route: runtime.route,
		message: runtime.opts.message, onProgress: onProgress, task: runtime.task,
	})
}

type codexControlledTurnOptions struct {
	ctx        context.Context
	agent      agent.Agent
	route      codexConversationRoute
	message    string
	onProgress func(agent.ProgressEvent)
	task       *activeAgentTask
}

// runControlledCodexTurn 是所有消息入口启动 Codex turn 的唯一业务层出口。
func (h *Handler) runControlledCodexTurn(opts codexControlledTurnOptions) (string, error) {
	liveAgent, ok := opts.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return h.chatWithAgentWithProgressEvents(
			opts.ctx, opts.agent, opts.route.conversationID, opts.message, opts.onProgress,
		)
	}
	request := h.buildCodexRuntimeRequestForTurn(opts.route, opts.route.threadID)
	return liveAgent.RunCodexTurn(opts.ctx, agent.CodexTurnRequest{
		Runtime: request, Message: opts.message, OnProgressEvent: opts.onProgress,
		OnThreadReplaced: func(previous agent.CodexThreadRef, current agent.CodexThreadRef) error {
			return h.commitCodexFirstTurnReplacement(opts, previous, current)
		},
		OnTurnStarted: func(thread agent.CodexThreadRef, turnID string) error {
			trace, traceErr := opts.task.setTraceThreadTurn(thread.ThreadID, turnID)
			h.recordTraceStage(trace, "turn.started", "running", "Codex turn accepted")
			if traceErr != nil {
				log.Printf("[terminal-outbox] 首次 turn trace 暂未持久化，将在 follower 恢复时重试: %v", traceErr)
			}
			if err := h.claimCodexFollowerTurnForTask(
				opts.route.bindingKey, opts.route.conversationID, thread.ThreadID, turnID, opts.task,
			); err != nil {
				return err
			}
			if thread.ConversationID == opts.route.conversationID {
				h.ensureCodexSessions().clearPendingFirstTurn(
					opts.route.bindingKey, opts.route.workspaceRoot, thread.ThreadID,
				)
				if traceErr == nil {
					h.ensureCodexSessions().clearFirstTurnRecoveryJournal(
						opts.route.bindingKey, opts.route.workspaceRoot, thread.ThreadID,
						opts.route.threadID, opts.task.activeRecoveryReservationID(),
					)
				}
			}
			return nil
		},
	})
}

func (h *Handler) commitCodexFirstTurnReplacement(
	opts codexControlledTurnOptions,
	previous agent.CodexThreadRef,
	current agent.CodexThreadRef,
) error {
	if previous.ConversationID != opts.route.conversationID || current.ConversationID != opts.route.conversationID ||
		previous.ThreadID != opts.route.threadID {
		return fmt.Errorf("Codex 首次写入 thread 替换与当前路由不一致")
	}
	unlockBinding, err := h.lockCodexSessionBinding(
		opts.ctx, opts.route.bindingKey, "first-turn-replace",
	)
	if err != nil {
		return err
	}
	defer unlockBinding()
	unlock, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
		ctx: opts.ctx, command: "first-turn-replace",
		threadIDs: []string{previous.ThreadID, current.ThreadID},
	})
	if err != nil {
		return err
	}
	defer unlock()
	recoveryReservationID := opts.task.activeRecoveryReservationID()
	if err := h.ensureCodexSessions().replaceRemoteFirstTurnThread(
		opts.route.bindingKey, opts.route.workspaceRoot, opts.route.conversationID,
		previous.ThreadID, current.ThreadID, recoveryReservationID,
	); err != nil {
		return err
	}
	if opts.task != nil {
		if traceErr := opts.task.replaceCodexThread(previous.ThreadID, current.ThreadID); traceErr != nil {
			log.Printf("[terminal-outbox] 首次 thread 替换 trace 暂未持久化，将保留恢复前驱: %v", traceErr)
		}
	}
	return nil
}

func codexTaskOwnerSnapshot(ag agent.Agent) (agent.CodexRuntimeHolder, uint64) {
	if _, ok := ag.(agent.CodexLiveRuntimeAgent); !ok {
		return agent.CodexRuntimeWeClaw, 0
	}
	return agent.CodexRuntimeUnknown, 0
}
