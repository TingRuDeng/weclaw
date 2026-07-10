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

func (h *Handler) startExternalCodexTaskIfActive(opts externalCodexTaskOptions) (agent.CodexThreadState, bool, error) {
	runtimeAg, ok := opts.agent.(agent.CodexThreadRuntimeAgent)
	if !ok {
		return agent.CodexThreadState{}, false, nil
	}
	state, err := runtimeAg.ReadCodexThreadState(opts.ctx, opts.conversationID, opts.threadID)
	if err != nil {
		return state, false, err
	}
	if !state.Active {
		return state, false, nil
	}
	if state.ActiveTurnID == "" {
		return state, false, fmt.Errorf("Codex App thread 处于 active 状态，但未找到 active turn")
	}
	if opts.reply == nil {
		return state, false, fmt.Errorf("Codex App thread 正在运行，但当前入口无法接管回推")
	}
	h.startExternalCodexTaskWatcher(opts, state, runtimeAg)
	return state, true, nil
}

func renderExternalCodexActiveNotice(state agent.CodexThreadState) []string {
	lines := []string{"Codex App 任务正在进行。"}
	if state.Preview != "" {
		lines = append(lines, "任务: "+previewPendingCodexMessage(state.Preview))
	}
	lines = append(lines, "新消息会先暂存；回复 /guide 发送到当前任务，回复 /cancel 撤回。")
	return lines
}

func renderExternalCodexStateReadError(err error) []string {
	if err == nil {
		return nil
	}
	return []string{"Codex App 当前任务状态读取失败: " + err.Error()}
}

func (h *Handler) startExternalCodexTaskWatcher(opts externalCodexTaskOptions, state agent.CodexThreadState, runtimeAg agent.CodexThreadRuntimeAgent) {
	taskCtx := agent.ContextWithApprovalHandler(context.Background(), h.approvalHandlerForUser(opts.actorUserID, opts.routeUserID, opts.reply))
	task, watchCtx, started := h.beginActiveTask(taskCtx, opts.conversationID, activeTaskMeta{
		owner:         opts.actorUserID,
		agentName:     opts.agentName,
		message:       firstNonBlank(state.Preview, "Codex App 本地任务"),
		externalCodex: true,
		codexThreadID: opts.threadID,
		codexTurnID:   state.ActiveTurnID,
	})
	if !started {
		return
	}
	go h.runExternalCodexTaskWatcher(externalCodexTaskRuntime{
		opts:      opts,
		state:     state,
		runtimeAg: runtimeAg,
		task:      task,
		ctx:       watchCtx,
	})
}

type externalCodexTaskRuntime struct {
	opts      externalCodexTaskOptions
	state     agent.CodexThreadState
	runtimeAg agent.CodexThreadRuntimeAgent
	task      *activeAgentTask
	ctx       context.Context
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
	reply, err := runtime.runtimeAg.WatchCodexThread(runtime.ctx, runtime.opts.conversationID, runtime.opts.threadID, recordProgress)
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

func (h *Handler) finishExternalCodexTask(runtime externalCodexTaskRuntime) {
	message, ok := h.completeActiveTask(runtime.opts.conversationID, runtime.task)
	if ok {
		sendPlatformText(runtime.opts.ctx, runtime.opts.reply, runtime.opts.actorUserID, runnablePendingCodexPrompt(message))
	}
}
