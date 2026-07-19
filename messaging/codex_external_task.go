package messaging

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

type externalCodexTaskOptions struct {
	ctx            context.Context
	actorUserID    string
	routeUserID    string
	agentName      string
	agent          agent.Agent
	conversationID string
	threadID       string
	workspaceRoot  string
	platform       platform.PlatformName
	accountID      string
	progressCfg    config.ProgressConfig
	reply          platform.Replier
	// runtimeInactiveAuthoritative 仅用于同一绑定事务内的 active→terminal 二次确认。
	runtimeInactiveAuthoritative bool
}

type externalCodexTaskState struct {
	agent.CodexThreadState
	Progress     string
	Controllable bool
}

type externalCodexTaskWatch func(context.Context, func(agent.ProgressEvent)) (string, error)

type resolvedExternalCodexTask struct {
	state             externalCodexTaskState
	watch             externalCodexTaskWatch
	active            bool
	confirmedInactive bool
	err               error
}

// startExternalCodexTaskIfActive 在切换会话后登记可持续回推的外部任务。
func (h *Handler) startExternalCodexTaskIfActive(opts externalCodexTaskOptions) (externalCodexTaskState, bool, error) {
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil || !prepared.active {
		return prepared.state, false, err
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		return prepared.state, false, err
	}
	if !h.activateExternalCodexTaskReservation(reservation) {
		return prepared.state, false, errExternalCodexTaskReservationConflict
	}
	return prepared.state, true, nil
}

// resolveExternalCodexTask 优先使用可控制的 runtime，活动 rollout 作为跨进程补充证据。
func (h *Handler) resolveExternalCodexTask(opts externalCodexTaskOptions) resolvedExternalCodexTask {
	runtime := resolveExternalCodexRuntime(opts)
	if runtime.active && runtime.state.ActiveTurnID != "" ||
		opts.runtimeInactiveAuthoritative && runtime.confirmedInactive {
		return runtime
	}
	rollout := h.resolveExternalCodexRollout(opts.threadID)
	if rollout.err != nil || rollout.active {
		return rollout
	}
	if runtime.active || runtime.err != nil {
		return runtime
	}
	return rollout
}

// resolveExternalCodexRuntime 二次读取可控制 runtime，并显式记录 inactive 证据。
func resolveExternalCodexRuntime(opts externalCodexTaskOptions) resolvedExternalCodexTask {
	runtimeAg, ok := opts.agent.(agent.CodexThreadRuntimeAgent)
	if !ok {
		return resolvedExternalCodexTask{}
	}
	state, err := runtimeAg.ReadCodexThreadState(opts.ctx, opts.conversationID, opts.threadID)
	resolved := resolvedExternalCodexTask{
		state: externalCodexTaskState{CodexThreadState: state, Controllable: true},
		err:   err,
	}
	if err != nil {
		return resolved
	}
	if !state.Active {
		resolved.confirmedInactive = true
		return resolved
	}
	resolved.active = true
	if state.ActiveTurnID != "" {
		if structured, ok := opts.agent.(agent.CodexStructuredThreadRuntimeAgent); ok {
			resolved.watch = func(ctx context.Context, onProgress func(agent.ProgressEvent)) (string, error) {
				return structured.WatchCodexThreadEvents(ctx, opts.conversationID, opts.threadID, onProgress)
			}
		} else {
			resolved.watch = func(ctx context.Context, onProgress func(agent.ProgressEvent)) (string, error) {
				return runtimeAg.WatchCodexThread(ctx, opts.conversationID, opts.threadID, textProgressCallback(onProgress))
			}
		}
	}
	return resolved
}

// resolveExternalCodexRollout 读取共享 rollout，并只把真实文件中的终态当 inactive 证据。
func (h *Handler) resolveExternalCodexRollout(threadID string) resolvedExternalCodexTask {
	rollout, found, err := h.readLocalCodexRolloutTaskState(threadID)
	resolved := resolvedExternalCodexTask{err: err}
	if err != nil || !found {
		return resolved
	}
	if !rollout.Active {
		resolved.confirmedInactive = true
		return resolved
	}
	resolved.active = true
	resolved.state = externalCodexTaskState{
		CodexThreadState: agent.CodexThreadState{
			ThreadID: threadID, Active: true, ActiveTurnID: rollout.TurnID,
			Preview: firstNonBlank(rollout.Preview, "共享 Codex 任务"),
		},
		Progress: rollout.Progress,
	}
	resolved.watch = func(ctx context.Context, onProgress func(agent.ProgressEvent)) (string, error) {
		return watchCodexRolloutTask(ctx, rollout, textProgressCallback(onProgress))
	}
	return resolved
}

// renderExternalCodexActiveNotice 展示当前任务、最新进展和真实可用的控制方式。
func renderExternalCodexActiveNotice(state externalCodexTaskState) []string {
	lines := []string{"共享 Codex 任务正在进行。"}
	if state.Preview != "" {
		lines = append(lines, "任务: "+previewPendingCodexMessage(state.Preview))
	}
	if state.Progress != "" {
		lines = append(lines, "当前进展: "+previewPendingCodexMessage(state.Progress))
	}
	if state.Controllable {
		lines = append(lines, "新消息会先暂存；回复 /guide 发送到当前任务，回复 /stop 停止任务，回复 /cancel 撤回暂存。")
	} else {
		lines = append(lines, "任务完成后结果会自动返回当前会话。")
	}
	return lines
}

func externalCodexTaskOwner(state externalCodexTaskState) (agent.CodexRuntimeHolder, uint64) {
	if state.Controllable {
		return agent.CodexRuntimeWeClaw, 0
	}
	return agent.CodexRuntimeUnknown, 0
}

type externalCodexTaskRuntime struct {
	opts  externalCodexTaskOptions
	state externalCodexTaskState
	watch externalCodexTaskWatch
	task  *activeAgentTask
	ctx   context.Context
}

func (h *Handler) runExternalCodexTaskWatcher(runtime externalCodexTaskRuntime) {
	progressCfg := runtime.opts.progressCfg
	if progressCfg.Mode == "" {
		progressCfg = h.resolveProgressConfigForAccount(runtime.opts.platform, runtime.opts.accountID, runtime.opts.agentName)
	}
	taskText := firstNonBlank(runtime.state.Preview, "共享 Codex 任务")
	onProgress, finishProgress, progressSession := h.startProgressSessionForWorkspaceAgentWithHandle(
		runtime.ctx, runtime.opts.reply, "", runtime.opts.agentName, runtime.opts.workspaceRoot, taskText, progressCfg,
	)
	runtime.task.mu.Lock()
	runtime.task.trace = traceWithReply(runtime.task.trace, runtime.opts.reply)
	runtime.task.mu.Unlock()
	trace := runtime.task.traceSnapshot()
	recordProgress := func(event agent.ProgressEvent) {
		delta, recorded := runtime.task.recordProgress(time.Now(), event)
		if !recorded {
			return
		}
		if !runtime.task.shouldSendFinal() {
			return
		}
		h.recordProgressTrace(runtime.task.traceSnapshot(), event, delta)
		onProgress(delta)
	}
	if runtime.state.Progress != "" {
		recordProgress(agent.TextProgressEvent(runtime.state.Progress))
	}
	result := h.superviseExternalCodexWatch(runtime, recordProgress)
	if !result.Terminal && runtime.task.isStopping() {
		result.Terminal = true
		result.Failed = true
		if result.Err == nil {
			result.Err = context.Canceled
		}
	}
	if !result.Terminal {
		h.recordTraceStage(trace, "task.observer_disconnected", "unknown", "observer ended without authoritative terminal")
		_ = finishProgress("", false)
		return
	}
	if result.ConfirmedTerminal {
		if reconcileErr := h.reconcileExternalCodexTerminal(runtime, result); reconcileErr != nil {
			log.Printf("[codex-runtime] 外部任务终态同步失败 thread=%q turn=%q: %v",
				runtime.opts.threadID, runtime.state.ActiveTurnID, reconcileErr)
			if errors.Is(reconcileErr, agent.ErrCodexRuntimeConflict) {
				result.Final = ""
				result.Err = reconcileErr
				result.Failed = true
			}
		}
	}
	runtime.task.closeProgress()
	if !h.claimActiveTaskTerminal(runtime.opts.conversationID, runtime.task) {
		_ = finishProgress("", false)
		return
	}
	reply := renderFinalSuccess("", result.Final)
	if result.Failed {
		reply = renderFinalFailure("", result.Err)
		summary := "shared Codex task failed"
		if result.Err != nil {
			summary = result.Err.Error()
		}
		h.recordTraceStage(trace, "task.failed", "failed", summary)
	} else {
		h.recordTraceStage(trace, "task.completed", "completed", "shared Codex task completed")
	}
	if runtime.task.shouldSendFinal() {
		h.finishAndSendProgressReply(progressReplyDelivery{
			delivery: replyDeliveryRequest{
				ctx: runtime.opts.ctx, replyWriter: runtime.opts.reply,
				userID: runtime.opts.actorUserID, agentName: runtime.opts.agentName, reply: reply, trace: trace,
			},
			failed: result.Failed, finish: finishProgress, progress: progressSession,
		})
	} else {
		_ = finishProgress("", false)
	}
	pending, hasPending := h.finishClaimedActiveTask(runtime.opts.conversationID, runtime.task)
	if hasPending {
		pending.run()
	}
}

// reconcileExternalCodexTerminal 在消息层释放观察任务前，先收敛同一 turn 的运行态。
func (h *Handler) reconcileExternalCodexTerminal(runtime externalCodexTaskRuntime, result codexExternalWatchResult) error {
	turnID := strings.TrimSpace(runtime.state.ActiveTurnID)
	if !runtime.state.Controllable || turnID == "" {
		return nil
	}
	liveAgent, ok := runtime.opts.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return agent.ErrCodexRuntimeUnavailable
	}
	state := runtime.state.CodexThreadState
	state.Active = false
	state.ActiveTurnID = ""
	state.LastTurnID = turnID
	state.LastTurnStatus = "completed"
	state.WaitingOnApproval = false
	state.WaitingOnUserInput = false
	if result.Failed {
		state.LastTurnStatus = "failed"
	}
	if strings.TrimSpace(result.Final) != "" {
		state.LastAgentMessageText = result.Final
	}

	unlock := h.lockCodexThreadControl(runtime.opts.threadID)
	defer unlock()
	route := codexConversationRoute{
		bindingKey:     codexBindingKey(runtime.opts.routeUserID, runtime.opts.agentName),
		conversationID: runtime.opts.conversationID,
	}
	request := agent.CodexRuntimeRequest{
		Ref: agent.CodexThreadRef{
			ConversationID: runtime.opts.conversationID,
			ThreadID:       runtime.opts.threadID,
		},
		Intent: codexSharedHostIntent(route),
	}
	_, err := liveAgent.ReconcileCodexObservedTurn(runtime.opts.ctx, request, state)
	return err
}
