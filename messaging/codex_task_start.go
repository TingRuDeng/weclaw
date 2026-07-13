package messaging

import (
	"context"
	"fmt"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexTaskPreflightOptions struct {
	taskOpts codexAgentTaskOptions
	route    codexConversationRoute
	cancel   context.CancelFunc
}

// preflightCodexTaskStart 在登记新任务前确认实时 owner 和 active turn。
func (h *Handler) preflightCodexTaskStart(opts codexTaskPreflightOptions) bool {
	if opts.route.threadID == "" {
		return false
	}
	if _, ok := opts.taskOpts.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return false
	}
	resolution, err := h.resolveCodexRuntime(opts.taskOpts.ctx, codexRuntimeResolveOptions{
		route: opts.route, threadID: opts.route.threadID, ag: opts.taskOpts.agent,
	})
	if err != nil {
		h.rejectCodexTaskStart(opts, err)
		return true
	}
	if codexResolutionActive(resolution) {
		return h.queueMessageBehindLiveTask(opts)
	}
	if err := ensureCodexRuntimeReady(resolution); err != nil {
		h.rejectCodexTaskStart(opts, err)
		return true
	}
	return false
}

func codexResolutionActive(resolution codexRuntimeResolution) bool {
	return resolution.Binding.State.Active || resolution.Rollout.Active
}

func (h *Handler) queueMessageBehindLiveTask(opts codexTaskPreflightOptions) bool {
	taskOpts := opts.taskOpts
	_, active, err := h.startExternalCodexTaskIfActive(externalCodexTaskOptions{
		ctx: taskOpts.ctx, actorUserID: taskOpts.userID, routeUserID: taskOpts.routeUserID,
		agentName: taskOpts.agentName, agent: taskOpts.agent,
		conversationID: opts.route.conversationID, threadID: opts.route.threadID,
		progressCfg: taskOpts.progressCfg, reply: taskOpts.reply,
	})
	if err != nil {
		h.rejectCodexTaskStart(opts, err)
		return true
	}
	if !active {
		return false
	}
	opts.cancel()
	taskOpts.route = opts.route
	_, exists := h.activeTask(opts.route.conversationID)
	if !exists {
		return false
	}
	if h.storePendingGuide(opts.route.conversationID, h.pendingCodexTask(taskOpts)) {
		sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID, queuedAgentMessage)
		return true
	}
	sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID, "当前任务已有一条暂存消息，请先处理后再发送。")
	return true
}

func (h *Handler) rejectCodexTaskStart(opts codexTaskPreflightOptions, err error) {
	opts.cancel()
	message := fmt.Sprintf("当前 Codex 会话暂不能开始任务: %v", err)
	sendPlatformText(opts.taskOpts.ctx, opts.taskOpts.reply, opts.taskOpts.userID, message)
}
