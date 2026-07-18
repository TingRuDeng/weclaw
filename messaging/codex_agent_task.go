package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// startCodexAgentTask 先登记 active task 再后台执行，保证 /guide 和 /cancel 可及时进入 Handler。
func (h *Handler) startCodexAgentTask(opts codexAgentTaskOptions) {
	if strings.TrimSpace(opts.routeUserID) == "" {
		opts.routeUserID = opts.userID
	}
	bindingKey := codexBindingKey(opts.routeUserID, opts.agentName)
	unlockBinding := h.lockAgentExecution(codexBindingExecutionKey(bindingKey))
	defer unlockBinding()
	agentCtx, cancelTaskTimeout := contextWithTaskTimeout(opts.ctx, opts.progressCfg)
	agentCtx = h.withAgentInteractions(agentCtx, agentInteractionContextOptions{
		actorUserID: opts.userID, routeUserID: opts.routeUserID,
		agentName: opts.agentName, reply: opts.reply,
	})
	route := opts.route
	if route.conversationID == "" {
		route = h.codexConversationRouteForSession(opts.userID, opts.routeUserID, opts.agentName, opts.agent)
	}
	if !h.workspaceAllowedForAgentContext(opts.ctx, opts.agentName, route.workspaceRoot) {
		sendPlatformText(opts.ctx, opts.reply, opts.userID, "当前工作空间不在允许范围，请发送 /cx ls 重新选择。")
		cancelTaskTimeout()
		return
	}
	unlockControl := h.lockCodexThreadControl(route.threadID)
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
		codexThreadID: route.threadID, inProcessCodexLifecycle: true,
	}, h.pendingCodexTask(opts))
	if admission.status != activeTaskStarted {
		cancelTaskTimeout()
		replyAgentTaskAdmission(agentTaskAdmissionNotice{
			ctx: opts.ctx, reply: opts.reply, userID: opts.userID,
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
		userID: opts.userID, agentName: opts.agentName, message: opts.message,
		replyPrefix: opts.replyPrefix, progressConfig: opts.progressCfg,
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
func (h *Handler) executeCodexAgentTurn(runtime codexAgentTaskRuntime, onProgress func(string)) (string, error) {
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
func (h *Handler) runCodexAgentTurn(runtime codexAgentTaskRuntime, onProgress func(string)) (string, error) {
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
	onProgress func(string)
	task       *activeAgentTask
}

// runControlledCodexTurn 是所有消息入口启动 Codex turn 的唯一业务层出口。
func (h *Handler) runControlledCodexTurn(opts codexControlledTurnOptions) (string, error) {
	liveAgent, ok := opts.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return h.chatWithAgentWithProgress(
			opts.ctx, opts.agent, opts.route.conversationID, opts.message, opts.onProgress,
		)
	}
	request := h.buildCodexRuntimeRequestForTurn(opts.route, opts.route.threadID)
	return liveAgent.RunCodexTurn(opts.ctx, agent.CodexTurnRequest{
		Runtime: request, Message: opts.message, OnProgress: opts.onProgress,
		OnThreadReplaced: func(previous agent.CodexThreadRef, current agent.CodexThreadRef) error {
			return h.commitCodexFirstTurnReplacement(opts, previous, current)
		},
		OnTurnStarted: func(thread agent.CodexThreadRef, _ string) error {
			if thread.ConversationID == opts.route.conversationID {
				h.ensureCodexSessions().clearPendingFirstTurn(
					opts.route.bindingKey, opts.route.workspaceRoot, thread.ThreadID,
				)
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
	unlock, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
		ctx: opts.ctx, command: "first-turn-replace",
		threadIDs: []string{previous.ThreadID, current.ThreadID},
	})
	if err != nil {
		return err
	}
	defer unlock()
	if err := h.ensureCodexSessions().replaceRemoteFirstTurnThread(
		opts.route.bindingKey, opts.route.workspaceRoot, opts.route.conversationID,
		previous.ThreadID, current.ThreadID,
	); err != nil {
		return err
	}
	if opts.task != nil {
		opts.task.replaceCodexThread(previous.ThreadID, current.ThreadID)
	}
	return nil
}

func codexTaskOwnerSnapshot(ag agent.Agent) (agent.CodexRuntimeHolder, uint64) {
	if _, ok := ag.(agent.CodexLiveRuntimeAgent); !ok {
		return agent.CodexRuntimeWeClaw, 0
	}
	return agent.CodexRuntimeUnknown, 0
}
