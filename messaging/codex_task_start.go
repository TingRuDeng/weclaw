package messaging

import (
	"context"
	"fmt"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
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
	resolution, err := h.resolveCodexRuntimeLocked(opts.taskOpts.ctx, codexRuntimeResolveOptions{
		route: opts.route, threadID: opts.route.threadID, ag: opts.taskOpts.agent,
	})
	if err != nil {
		h.rejectCodexTaskStart(opts, err)
		return true
	}
	if err := ensureCodexRouteOwnsControl(resolution.Request.Intent, opts.route); err != nil {
		h.rejectCodexOwnerTaskStart(opts, err)
		return true
	}
	if codexResolutionActive(resolution) {
		return h.queueMessageBehindLiveTask(opts)
	}
	resolution, err = h.realizePersistedCodexRemoteRuntime(opts, resolution)
	if err != nil {
		h.rejectCodexOwnerTaskStart(opts, err)
		return true
	}
	if codexResolutionActive(resolution) {
		return h.queueMessageBehindLiveTask(opts)
	}
	if err := ensureCodexRuntimeReady(resolution, opts.route); err != nil {
		h.rejectCodexOwnerTaskStart(opts, err)
		return true
	}
	return false
}

// realizePersistedCodexRemoteRuntime 恢复当前窗口已明确持久化的 remote 控制意图。
func (h *Handler) realizePersistedCodexRemoteRuntime(opts codexTaskPreflightOptions, resolution codexRuntimeResolution) (codexRuntimeResolution, error) {
	if !resolution.Live || resolution.Binding.Runtime != agent.CodexRuntimeUnknown {
		return resolution, nil
	}
	liveAgent, ok := opts.taskOpts.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return resolution, agent.ErrCodexRuntimeUnavailable
	}
	binding, err := liveAgent.HandoffCodexRuntime(opts.taskOpts.ctx, resolution.Request)
	resolution.Binding = binding
	resolution.ProbeErr = err
	return resolution, err
}

// rejectCodexOwnerTaskStart 在飞书中返回可直接操作的控制权卡片。
func (h *Handler) rejectCodexOwnerTaskStart(opts codexTaskPreflightOptions, err error) {
	opts.cancel()
	message := fmt.Sprintf("当前 Codex 会话暂不能开始任务: %v", err)
	taskOpts := opts.taskOpts
	if taskOpts.platform != platform.PlatformFeishu || taskOpts.reply == nil || !taskOpts.reply.Capabilities().Buttons {
		sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID, message)
		return
	}
	metadata := map[string]string{}
	if sessionKey := feishuSessionKeyFromRoute(taskOpts.routeUserID); sessionKey != "" {
		metadata[feishuSessionMetadataKey] = sessionKey
	}
	choices := platformChoicesWithMetadata([]platform.Choice{
		{ID: "/cx owner remote", Label: "交给当前远程窗口"},
		{ID: "/cx owner desktop", Label: "交给 Codex Desktop"},
	}, metadata)
	if askErr := taskOpts.reply.AskChoices(taskOpts.ctx, message, choices); askErr != nil {
		sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID, message)
	}
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
	taskOpts.route = opts.route
	status := h.queuePendingActiveTask(opts.route.conversationID, h.pendingCodexTask(taskOpts))
	if status == activeTaskMissing {
		return false
	}
	opts.cancel()
	if status == activeTaskQueued {
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
