package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
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
	reply          platform.Replier
}

type externalCodexTaskState struct {
	agent.CodexThreadState
	Progress     string
	Controllable bool
}

type externalCodexTaskWatch func(context.Context, func(string)) (string, error)

// startExternalCodexTaskIfActive 在切换会话后登记可持续回推的外部任务。
func (h *Handler) startExternalCodexTaskIfActive(opts externalCodexTaskOptions) (externalCodexTaskState, bool, error) {
	state, watch, found, err := h.resolveExternalCodexTask(opts)
	if err != nil || !found {
		return state, false, err
	}
	if state.ActiveTurnID == "" {
		return state, false, fmt.Errorf("codex App thread 处于 active 状态，但未找到 active turn")
	}
	if opts.reply == nil {
		return state, false, fmt.Errorf("codex App thread 正在运行，但当前入口无法接管回推")
	}
	h.startExternalCodexTaskWatcher(opts, state, watch)
	return state, true, nil
}

// resolveExternalCodexTask 优先使用可控制的 app-server，跨进程时改读共享 rollout。
func (h *Handler) resolveExternalCodexTask(opts externalCodexTaskOptions) (externalCodexTaskState, externalCodexTaskWatch, bool, error) {
	var runtimeErr error
	var incompleteRuntimeState *externalCodexTaskState
	if runtimeAg, ok := opts.agent.(agent.CodexThreadRuntimeAgent); ok {
		runtimeState, err := runtimeAg.ReadCodexThreadState(opts.ctx, opts.conversationID, opts.threadID)
		runtimeErr = err
		if err == nil && runtimeState.Active && runtimeState.ActiveTurnID != "" {
			state := externalCodexTaskState{CodexThreadState: runtimeState, Controllable: true}
			watch := func(ctx context.Context, onProgress func(string)) (string, error) {
				return runtimeAg.WatchCodexThread(ctx, opts.conversationID, opts.threadID, onProgress)
			}
			return state, watch, true, nil
		}
		if err == nil && runtimeState.Active {
			state := externalCodexTaskState{CodexThreadState: runtimeState, Controllable: true}
			incompleteRuntimeState = &state
		}
	}
	rollout, rolloutFound, rolloutErr := h.readLocalCodexRolloutTaskState(opts.threadID)
	if rolloutErr != nil {
		return externalCodexTaskState{}, nil, false, rolloutErr
	}
	if rolloutFound && rollout.Active {
		state := externalCodexTaskState{
			CodexThreadState: agent.CodexThreadState{
				ThreadID:     opts.threadID,
				Active:       true,
				ActiveTurnID: rollout.TurnID,
				Preview:      firstNonBlank(rollout.Preview, "Codex App 本地任务"),
			},
			Progress: rollout.Progress,
		}
		watch := func(ctx context.Context, onProgress func(string)) (string, error) {
			return watchCodexRolloutTask(ctx, rollout, onProgress)
		}
		return state, watch, true, nil
	}
	if incompleteRuntimeState != nil {
		return *incompleteRuntimeState, nil, true, nil
	}
	if runtimeErr != nil {
		return externalCodexTaskState{}, nil, false, runtimeErr
	}
	return externalCodexTaskState{}, nil, false, nil
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
		lines = append(lines, "新消息会先暂存；回复 /guide 发送到当前任务，回复 /cancel 撤回。")
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

// startExternalCodexTaskWatcher 登记任务镜像并启动异步进度与终态回推。
func (h *Handler) startExternalCodexTaskWatcher(opts externalCodexTaskOptions, state externalCodexTaskState, watch externalCodexTaskWatch) {
	taskCtx := agent.ContextWithApprovalHandler(context.Background(), h.approvalHandlerForUser(opts.actorUserID, opts.routeUserID, opts.reply))
	task, watchCtx, started := h.beginActiveTask(taskCtx, opts.conversationID, activeTaskMeta{
		owner: opts.actorUserID, agentName: opts.agentName,
		message:       firstNonBlank(state.Preview, "Codex App 本地任务"),
		externalCodex: true, externalControl: state.Controllable,
		codexThreadID: opts.threadID, codexTurnID: state.ActiveTurnID,
	})
	if !started {
		return
	}
	if state.Progress != "" {
		task.recordProgress(time.Now(), state.Progress)
	}
	go h.runExternalCodexTaskWatcher(externalCodexTaskRuntime{
		opts: opts, state: state, watch: watch, task: task, ctx: watchCtx,
	})
}

type externalCodexTaskRuntime struct {
	opts  externalCodexTaskOptions
	state externalCodexTaskState
	watch externalCodexTaskWatch
	task  *activeAgentTask
	ctx   context.Context
}

func (h *Handler) runExternalCodexTaskWatcher(runtime externalCodexTaskRuntime) {
	defer h.finishExternalCodexTask(runtime)
	progressCfg := h.resolveProgressConfigForAccount(runtime.opts.platform, runtime.opts.accountID, runtime.opts.agentName)
	taskText := firstNonBlank(runtime.state.Preview, "Codex App 本地任务")
	onProgress, finishProgress := h.startProgressSessionWithFinal(runtime.ctx, runtime.opts.reply, "", taskText, progressCfg)
	recordProgress := func(delta string) {
		runtime.task.recordProgress(time.Now(), delta)
		onProgress(delta)
	}
	if runtime.state.Progress != "" {
		onProgress(runtime.state.Progress)
	}
	reply, err := runtime.watch(runtime.ctx, recordProgress)
	failed := err != nil
	if failed {
		reply = renderFinalFailure("", err)
	} else {
		reply = renderFinalSuccess("", reply)
	}
	if runtime.task.shouldSendFinal() {
		consumed := finishProgressWithReplyForPlatform(runtime.opts.reply, finishProgress, reply, failed)
		h.sendReplyWithMediaAfterStreamForRoute(runtime.opts.ctx, runtime.opts.reply, runtime.opts.actorUserID, runtime.opts.routeUserID, runtime.opts.agentName, reply, consumed)
		return
	}
	_ = finishProgress("", false)
}

// finishExternalCodexTask 清理任务镜像，并唤醒此前暂存的可执行消息。
func (h *Handler) finishExternalCodexTask(runtime externalCodexTaskRuntime) {
	pending, ok := h.completeActiveTask(runtime.opts.conversationID, runtime.task)
	if ok {
		pending.run()
	}
}
