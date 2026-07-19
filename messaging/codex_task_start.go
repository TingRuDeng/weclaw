package messaging

import (
	"context"
	"fmt"
	"log"

	"github.com/fastclaw-ai/weclaw/agent"
)

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
	resolution, err := h.resolveBoundCodexRuntimeLocked(codexRuntimeResolveOptions{
		route: opts.route, threadID: opts.route.threadID, ag: opts.taskOpts.agent,
	})
	if err != nil {
		log.Printf("[codex-task] 共享 host 运行时快照暂不可用 thread=%q: %v", opts.route.threadID, err)
		return false
	}
	if codexRuntimeReadyForRemoteTurn(resolution.Binding.Runtime) && codexResolutionActive(resolution) {
		return h.queueMessageBehindLiveTask(opts)
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
	taskOpts.route = opts.route
	status := h.queuePendingActiveTask(opts.route.conversationID, h.pendingCodexTask(taskOpts))
	h.recordTaskAdmissionTrace(taskOpts.trace, status)
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
