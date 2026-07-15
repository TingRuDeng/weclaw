package messaging

import (
	"context"
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
	platform       platform.PlatformName
	accountID      string
	progressCfg    config.ProgressConfig
	reply          platform.Replier
	// runtimeInactiveAuthoritative 仅用于同一接管事务内的 active→terminal 二次确认。
	runtimeInactiveAuthoritative bool
}

type externalCodexTaskState struct {
	agent.CodexThreadState
	Progress     string
	Controllable bool
}

type externalCodexTaskWatch func(context.Context, func(string)) (string, error)

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
		resolved.watch = func(ctx context.Context, onProgress func(string)) (string, error) {
			return runtimeAg.WatchCodexThread(ctx, opts.conversationID, opts.threadID, onProgress)
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
			Preview: firstNonBlank(rollout.Preview, "Codex App 本地任务"),
		},
		Progress: rollout.Progress,
	}
	resolved.watch = func(ctx context.Context, onProgress func(string)) (string, error) {
		return watchCodexRolloutTask(ctx, rollout, onProgress)
	}
	return resolved
}

// renderExternalCodexActiveNotice 展示当前任务、最新进展和真实可用的控制方式。
func renderExternalCodexActiveNotice(state externalCodexTaskState) []string {
	lines := []string{"Codex App 任务正在进行。"}
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

// renderExternalCodexStateReadError 将状态读取错误显式反馈给切换用户。
func renderExternalCodexStateReadError(err error) []string {
	if err == nil {
		return nil
	}
	return []string{"Codex App 当前任务状态读取失败: " + err.Error()}
}

func externalCodexTaskOwner(state externalCodexTaskState) (agent.CodexRuntimeHolder, uint64) {
	if state.Controllable {
		return agent.CodexRuntimeDesktop, 0
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
	taskText := firstNonBlank(runtime.state.Preview, "Codex App 本地任务")
	onProgress, finishProgress := h.startProgressSessionWithFinal(runtime.ctx, runtime.opts.reply, "", taskText, progressCfg)
	recordProgress := func(delta string) {
		runtime.task.recordProgress(time.Now(), delta)
		onProgress(delta)
	}
	if runtime.state.Progress != "" {
		onProgress(runtime.state.Progress)
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
		_ = finishProgress("", false)
		return
	}
	if !h.claimActiveTaskTerminal(runtime.opts.conversationID, runtime.task) {
		_ = finishProgress("", false)
		return
	}
	reply := renderFinalSuccess("", result.Final)
	if result.Failed {
		reply = renderFinalFailure("", result.Err)
	}
	if runtime.task.shouldSendFinal() {
		h.finishAndSendProgressReply(progressReplyDelivery{
			delivery: replyDeliveryRequest{
				ctx: runtime.opts.ctx, replyWriter: runtime.opts.reply,
				userID: runtime.opts.actorUserID, agentName: runtime.opts.agentName, reply: reply,
			},
			failed: result.Failed, finish: finishProgress,
		})
	} else {
		_ = finishProgress("", false)
	}
	pending, hasPending := h.finishClaimedActiveTask(runtime.opts.conversationID, runtime.task)
	if hasPending {
		pending.run()
	}
}
