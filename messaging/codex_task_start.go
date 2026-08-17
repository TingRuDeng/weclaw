package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/fastclaw-ai/weclaw/agent"
)

var errCodexFollowerAttachPreparing = errors.New("Codex 会话正在切换中")

type codexTaskPreflightOptions struct {
	taskOpts codexAgentTaskOptions
	route    codexConversationRoute
	cancel   context.CancelFunc
}

// preflightCodexTaskStart 在登记新任务前读取共享 host 的已有 active turn。
// frontend binding 不再执行 owner 检查，也不会弹出控制权选择卡。
func (h *Handler) preflightCodexTaskStart(opts codexTaskPreflightOptions) bool {
	if opts.route.threadID == "" {
		return false
	}
	if _, ok := opts.taskOpts.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return false
	}
	if h.ensureCodexSessions().isPendingFirstTurn(
		opts.route.bindingKey, opts.route.workspaceRoot, opts.route.threadID,
	) {
		return false
	}
	resolution, err := h.resolveBoundCodexRuntimeLocked(codexRuntimeResolveOptions{
		route: opts.route, threadID: opts.route.threadID, ag: opts.taskOpts.agent,
	})
	if err != nil {
		log.Printf("[codex-task] 共享 host 运行时快照暂不可用 thread=%q: %v", opts.route.threadID, err)
		h.rejectCodexTaskStart(opts, err)
		return true
	}
	switch resolution.Binding.Runtime {
	case agent.CodexRuntimeDesktop, agent.CodexRuntimeWeClaw:
		if codexResolutionActive(resolution) {
			return h.steerMessageIntoLiveTask(opts)
		}
		return false
	case agent.CodexRuntimeConflict:
		h.rejectCodexTaskStart(opts, agent.ErrCodexRuntimeConflict)
	default:
		h.rejectCodexTaskStart(opts, agent.ErrCodexRuntimeUnavailable)
	}
	return true
}

func codexResolutionActive(resolution codexRuntimeResolution) bool {
	return resolution.Binding.State.Active || resolution.Rollout.Active
}

// steerMessageIntoLiveTask submits accepted input directly to the canonical
// app-server turn. No WeClaw-private pending queue sits between equal frontends.
func (h *Handler) steerMessageIntoLiveTask(opts codexTaskPreflightOptions) bool {
	taskOpts := opts.taskOpts
	state, active, err := h.startExternalCodexTaskIfActive(externalCodexTaskOptions{
		ctx: taskOpts.ctx, actorUserID: taskOpts.userID, routeUserID: taskOpts.routeUserID,
		agentName: taskOpts.agentName, agent: taskOpts.agent,
		conversationID: opts.route.conversationID, bindingKey: opts.route.bindingKey,
		threadID:      opts.route.threadID,
		workspaceRoot: opts.route.workspaceRoot,
		progressCfg:   taskOpts.progressCfg, reply: taskOpts.reply,
	})
	if err != nil {
		h.rejectCodexTaskStart(opts, err)
		return true
	}
	if !active {
		return false
	}
	runtimeAgent, ok := taskOpts.agent.(agent.CodexThreadRuntimeAgent)
	if !ok {
		h.rejectCodexTaskStart(opts, agent.ErrCodexRuntimeUnavailable)
		return true
	}
	opts.cancel()
	if err := runtimeAgent.SteerCodexThread(
		taskOpts.ctx, opts.route.conversationID, opts.route.threadID, state.ActiveTurnID, taskOpts.message,
	); err != nil {
		sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID,
			"发送到当前共享 Codex 任务失败: "+sanitizeAgentError(err.Error()))
		return true
	}
	h.recordTraceStage(taskOpts.trace.WithConversation(opts.route.conversationID).
		WithThreadTurn(opts.route.threadID, state.ActiveTurnID), "task.input_accepted", "running", "input steered to active Codex turn")
	delivery := codexGuideDeliveryResult{ReplyText: codexGuideAcceptedReply}
	if task, ok := h.activeTask(opts.route.conversationID); ok {
		delivery = h.completeAcceptedCodexGuide(
			taskOpts.ctx, task, taskOpts.reply, taskOpts.messageKey, "已接收新的补充输入。",
		)
	}
	if delivery.ReplyText != "" {
		sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID, delivery.ReplyText)
	}
	return true
}

func (h *Handler) rejectCodexTaskStart(opts codexTaskPreflightOptions, err error) {
	opts.cancel()
	if errors.Is(err, errCodexFollowerAttachPreparing) {
		sendPlatformText(opts.taskOpts.ctx, opts.taskOpts.reply, opts.taskOpts.userID,
			"Codex 会话正在切换中，请稍后重试。")
		return
	}
	if errors.Is(err, agent.ErrCodexRuntimeUnavailable) ||
		errors.Is(err, agent.ErrCodexDesktopOwnershipUnknown) ||
		errors.Is(err, agent.ErrCodexDesktopDisconnected) {
		sendPlatformText(opts.taskOpts.ctx, opts.taskOpts.reply, opts.taskOpts.userID,
			"Codex App 运行通道暂不可用，会话绑定已保留。请稍后重试，或发送 /cx status 查看状态。")
		return
	}
	message := fmt.Sprintf("当前 Codex 会话暂不能开始任务: %v", err)
	sendPlatformText(opts.taskOpts.ctx, opts.taskOpts.reply, opts.taskOpts.userID, message)
}
