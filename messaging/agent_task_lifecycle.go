package messaging

import (
	"context"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

type agentTaskLifecycleOptions struct {
	taskCtx        context.Context
	replyCtx       context.Context
	reply          platform.Replier
	task           *activeAgentTask
	cancel         context.CancelFunc
	executionKey   string
	userID         string
	agentName      string
	message        string
	replyPrefix    string
	progressConfig config.ProgressConfig
}

type agentTaskLifecycle struct {
	opts       agentTaskLifecycleOptions
	onProgress func(string)
	finish     func(string, bool) bool
}

type agentTaskAdmissionNotice struct {
	ctx    context.Context
	reply  platform.Replier
	userID string
}

// startAgentTaskLifecycle 创建三类 Agent 共用的进度和终态交付器。
func (h *Handler) startAgentTaskLifecycle(opts agentTaskLifecycleOptions) agentTaskLifecycle {
	onProgress, finish := h.startProgressSessionWithFinal(
		opts.taskCtx, opts.reply, opts.replyPrefix, opts.message, opts.progressConfig,
	)
	return agentTaskLifecycle{opts: opts, onProgress: onProgress, finish: finish}
}

// recordProgress 同时更新任务状态快照和平台进度展示。
func (l agentTaskLifecycle) recordProgress(delta string) {
	l.opts.task.recordProgress(time.Now(), delta)
	l.onProgress(delta)
}

// finishAgentTaskLifecycle 统一最终文本、进度卡收口和停止后的回复抑制。
func (h *Handler) finishAgentTaskLifecycle(lifecycle agentTaskLifecycle, reply string, err error) {
	if err != nil {
		reply = renderFinalFailure(lifecycle.opts.replyPrefix, err)
	} else {
		reply = renderFinalSuccess(lifecycle.opts.replyPrefix, reply)
	}
	if !lifecycle.opts.task.shouldSendFinal() {
		_ = lifecycle.finish("", false)
		return
	}
	h.finishAndSendProgressReply(progressReplyDelivery{
		delivery: replyDeliveryRequest{
			ctx: lifecycle.opts.replyCtx, replyWriter: lifecycle.opts.reply,
			userID: lifecycle.opts.userID, agentName: lifecycle.opts.agentName, reply: reply,
		},
		failed: err != nil, finish: lifecycle.finish,
	})
}

// completeAgentTaskLifecycle 清理活动任务，并异步续跑唯一一条暂存消息。
func (h *Handler) completeAgentTaskLifecycle(lifecycle agentTaskLifecycle) {
	lifecycle.opts.cancel()
	if pending, ok := h.completeActiveTask(lifecycle.opts.executionKey, lifecycle.opts.task); ok {
		go pending.run()
	}
}

// replyAgentTaskAdmission 统一三类 Agent 的排队和队列占用提示。
func replyAgentTaskAdmission(notice agentTaskAdmissionNotice, status activeTaskAdmissionStatus) {
	if status == activeTaskQueued {
		sendPlatformText(notice.ctx, notice.reply, notice.userID, queuedAgentMessage)
		return
	}
	sendPlatformText(notice.ctx, notice.reply, notice.userID, "当前任务已有一条暂存消息，请先处理后再发送。")
}
