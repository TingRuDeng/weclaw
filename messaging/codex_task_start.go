package messaging

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/google/uuid"
)

type codexTaskPreflightOptions struct {
	ctx      context.Context
	taskOpts codexAgentTaskOptions
	route    codexConversationRoute
	cancel   context.CancelFunc
}

// preflightCodexTaskStart only intercepts a message when this frontend already
// has a progress lifecycle for the same thread. A new frontend task always
// proceeds through RunCodexTurn, which performs the authoritative read and
// start/steer dispatch itself.
func (h *Handler) preflightCodexTaskStart(opts codexTaskPreflightOptions) bool {
	if opts.route.threadID == "" {
		return false
	}
	if _, ok := opts.taskOpts.agent.(agent.CodexInputSteeringAgent); !ok {
		return false
	}
	task, ok := h.activeTask(opts.route.conversationID)
	if !ok {
		return false
	}
	task.mu.Lock()
	sameThread := strings.TrimSpace(task.codexThreadID) == strings.TrimSpace(opts.route.threadID)
	task.mu.Unlock()
	if !sameThread {
		return false
	}
	return h.steerMessageIntoLiveTask(opts, task)
}

// steerMessageIntoLiveTask submits accepted input directly to the canonical
// app-server turn. No WeClaw-private pending queue sits between equal frontends.
func (h *Handler) steerMessageIntoLiveTask(opts codexTaskPreflightOptions, task *activeAgentTask) bool {
	taskOpts := opts.taskOpts
	steeringAgent, ok := taskOpts.agent.(agent.CodexInputSteeringAgent)
	if !ok {
		return false
	}
	submitCtx := opts.ctx
	if submitCtx == nil {
		submitCtx = taskOpts.ctx
	}
	_, err := steeringAgent.SteerCodexInput(submitCtx, h.codexSteerInputRequest(
		opts.route, task, taskOpts.message, taskOpts.messageKey,
	))
	if errors.Is(err, agent.ErrCodexNoActiveTurn) {
		if codexTaskUsesReadOnlySynchronization(task) {
			opts.cancel()
			sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID,
				"当前仅能同步 Codex 任务进度，输入未发送且不会排队。请在运行通道恢复后重新发送。")
			return true
		}
		select {
		case <-task.done:
			return false
		case <-submitCtx.Done():
			opts.cancel()
			sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID,
				"Codex Host 已空闲，但本地上一任务的终态同步尚未完成；输入未发送且不会排队，请重新发送。")
			return true
		}
	}
	opts.cancel()
	if err != nil {
		sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID,
			"发送到当前共享 Codex 任务失败: "+friendlyAgentError(err))
		return true
	}
	delivery := codexGuideDeliveryResult{ReplyText: codexGuideAcceptedReply}
	if task != nil {
		delivery = h.completeAcceptedCodexGuide(
			taskOpts.ctx, task, taskOpts.reply, taskOpts.messageKey, "已接收新的补充输入。",
		)
	}
	if delivery.ReplyText != "" {
		sendPlatformText(taskOpts.ctx, taskOpts.reply, taskOpts.userID, delivery.ReplyText)
	}
	return true
}

func codexTaskUsesReadOnlySynchronization(task *activeAgentTask) bool {
	if task == nil {
		return false
	}
	task.mu.Lock()
	control := task.externalReservation
	task.mu.Unlock()
	if control == nil {
		return false
	}
	control.mu.Lock()
	readOnly := !control.runtime.state.Controllable
	control.mu.Unlock()
	return readOnly
}

func (h *Handler) codexSteerInputRequest(
	route codexConversationRoute,
	task *activeAgentTask,
	message string,
	messageKey string,
) agent.CodexTurnRequest {
	request := h.buildCodexRuntimeRequestForTurn(route, route.threadID)
	return agent.CodexTurnRequest{
		Runtime: request, Message: message,
		AttemptID: uuid.NewString(), MessageKey: messageKey,
		OnInputAttempt: h.ensureCodexInputAttempts().record,
		OnTurnSteered: func(thread agent.CodexThreadRef, turnID string) error {
			trace, traceErr := task.setTraceThreadTurn(thread.ThreadID, turnID)
			h.recordTraceStage(trace, "task.input_accepted", "running", "input steered to active Codex turn")
			if traceErr != nil {
				log.Printf("[codex-task] 补充输入 trace 同步降级 thread=%q: %v", thread.ThreadID, traceErr)
			}
			if claimErr := h.claimCodexFollowerTurnForTask(
				route.bindingKey, route.conversationID, thread.ThreadID, turnID, task,
			); claimErr != nil {
				h.recordCodexFollowerSyncFailure(route.bindingKey, thread.ThreadID, claimErr)
				log.Printf("[codex-task] 补充输入 follower 同步降级 thread=%q: %v", thread.ThreadID, claimErr)
			}
			return nil
		},
	}
}
