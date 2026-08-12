package messaging

import (
	"context"
	"errors"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
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
	workspaceRoot  string
	message        string
	replyPrefix    string
	progressConfig config.ProgressConfig
	trace          observability.TraceContext
}

type agentTaskLifecycle struct {
	handler    *Handler
	opts       agentTaskLifecycleOptions
	onProgress func(string)
	finish     func(string, bool) bool
	progress   *progressSession
}

type agentTaskAdmissionNotice struct {
	ctx            context.Context
	platformName   platform.PlatformName
	accountID      string
	reply          platform.Replier
	userID         string
	routeUserID    string
	agentName      string
	executionKey   string
	task           *activeAgentTask
	guideSupported bool
}

// startAgentTaskLifecycle 创建三类 Agent 共用的进度和终态交付器。
func (h *Handler) startAgentTaskLifecycle(opts agentTaskLifecycleOptions) agentTaskLifecycle {
	if opts.task != nil {
		opts.task.setProgressTimelineLimit(opts.progressConfig.EffectiveStreamTimelineLimit())
		opts.trace = traceWithReply(opts.task.traceSnapshot(), opts.reply)
		opts.task.mu.Lock()
		opts.task.trace = opts.trace
		opts.task.mu.Unlock()
	}
	opts.taskCtx = observability.ContextWithTrace(opts.taskCtx, opts.trace)
	guard := h.codexFollowerDeliveryGuardForTask(opts.task)
	onProgress, finish, progress := h.startProgressSessionForWorkspaceAgentWithGuard(
		opts.taskCtx, opts.reply, opts.replyPrefix, opts.agentName, opts.workspaceRoot, opts.message, opts.progressConfig,
		guard,
	)
	if opts.task != nil {
		opts.task.attachProgressSession(progress)
	}
	h.recordTraceStage(opts.trace, "task.started", "running", "agent="+opts.agentName)
	return agentTaskLifecycle{handler: h, opts: opts, onProgress: onProgress, finish: finish, progress: progress}
}

// recordProgress 同时更新唯一结构化任务快照和平台进度展示。
func (l agentTaskLifecycle) recordProgress(event agent.ProgressEvent) {
	update, recorded := l.opts.task.recordProgressUpdate(time.Now(), event)
	if !recorded {
		return
	}
	if !l.opts.task.shouldSendFinal() {
		return
	}
	if l.handler != nil {
		l.handler.recordProgressTrace(l.opts.task.traceSnapshot(), event, update.latest)
	}
	if l.progress != nil {
		l.progress.onTaskProgress(update)
		return
	}
	l.onProgress(update.latest)
}

// finishAgentTaskLifecycle 统一最终文本、进度卡收口和停止后的回复抑制。
func (h *Handler) finishAgentTaskLifecycle(lifecycle agentTaskLifecycle, reply string, err error) {
	defer lifecycle.opts.task.detachProgressSession(lifecycle.progress)
	if errors.Is(err, agent.ErrCodexObserverDetached) {
		trace := lifecycle.opts.task.traceSnapshot()
		h.recordTraceStage(trace, "task.observer_preserved", "detached", "observer detached without stopping shared Codex turn")
		if lifecycle.opts.task.shouldPreserveRecoveryOnDrain() && lifecycle.progress != nil {
			lifecycle.progress.stopBackground()
		}
		h.finishActiveTask(lifecycle.opts.executionKey, lifecycle.opts.task)
		return
	}
	lifecycle.opts.task.closeProgress()
	trace := lifecycle.opts.task.traceSnapshot()
	stopped := lifecycle.opts.task.stoppedByRequest(err)
	if stopped {
		lifecycle.opts.task.recordTerminalView(time.Now(), "stopped")
		reply = renderFinalStopped(lifecycle.opts.replyPrefix)
		h.recordTraceStage(trace, "task.stopped", "stopped", "task stopped by user request")
	} else if err != nil {
		lifecycle.opts.task.recordTerminalView(time.Now(), "failed")
		reply = renderFinalFailure(lifecycle.opts.replyPrefix, err)
		h.recordTraceStage(trace, "task.failed", "failed", err.Error())
	} else {
		lifecycle.opts.task.recordTerminalView(time.Now(), "completed")
		reply = renderFinalSuccess(lifecycle.opts.replyPrefix, reply)
		h.recordTraceStage(trace, "task.completed", "completed", "agent task completed")
	}
	if !h.claimActiveTaskTerminal(lifecycle.opts.executionKey, lifecycle.opts.task) {
		h.recordTraceStage(trace, "task.delivery_suppressed", "detached", "final reply suppressed")
		if stopped && lifecycle.progress != nil {
			_ = lifecycle.progress.stopWithTerminal("", false, true)
		} else {
			_ = lifecycle.finish("", false)
		}
		return
	}
	h.finishAndSendProgressReply(progressReplyDelivery{
		delivery: replyDeliveryRequest{
			ctx: lifecycle.opts.replyCtx, replyWriter: lifecycle.opts.reply,
			userID: lifecycle.opts.userID, agentName: lifecycle.opts.agentName, reply: reply,
			trace: trace, deliveryGuard: lifecycle.opts.task.terminalDeliveryGuardSnapshot(),
		},
		failed: err != nil && !stopped, stopped: stopped,
		idempotencyKey: lifecycle.opts.task.terminalDeliveryKeySnapshot(),
		finish:         lifecycle.finish, progress: lifecycle.progress,
	})
}

// completeAgentTaskLifecycle 清理活动任务，并异步续跑唯一一条暂存消息。
func (h *Handler) completeAgentTaskLifecycle(lifecycle agentTaskLifecycle) {
	lifecycle.opts.cancel()
	pending, ok := h.finishClaimedActiveTask(lifecycle.opts.executionKey, lifecycle.opts.task)
	if !ok {
		pending, ok = h.completeActiveTask(lifecycle.opts.executionKey, lifecycle.opts.task)
	}
	if ok {
		go pending.run()
	}
}

// replyAgentTaskAdmission 统一三类 Agent 的排队和队列占用提示。
func (h *Handler) replyAgentTaskAdmission(notice agentTaskAdmissionNotice, status activeTaskAdmissionStatus) {
	if status == activeTaskDraining {
		sendPlatformText(notice.ctx, notice.reply, notice.userID, "WeClaw 正在安全重启，暂不接收新任务，请稍后重试。")
		return
	}
	if status == activeTaskQueued {
		if h.sendPendingTaskControlCard(notice) {
			return
		}
		sendPlatformText(notice.ctx, notice.reply, notice.userID, queuedAgentMessage)
		return
	}
	if status == activeTaskForeignWriter {
		sendPlatformText(notice.ctx, notice.reply, notice.userID, "当前 Claude session 正由另一个窗口执行任务，请等待该任务结束后重试。")
		return
	}
	sendPlatformText(notice.ctx, notice.reply, notice.userID, "当前任务已有一条暂存消息，请先处理后再发送。")
}
