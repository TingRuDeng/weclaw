package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const codexGuideAcceptedReply = "已发送到当前共享 Codex 任务。"

type codexGuideDeliveryResult struct {
	Reanchored bool
	ReplyText  string
}

func codexGuideTransitionID(taskID string, messageKey string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("weclaw-guide\x00"+taskID+"\x00"+messageKey)).String()
}

// completeAcceptedCodexGuide records an accepted steer and moves the progress
// authority to the triggering message when that platform supports native cards.
func (h *Handler) completeAcceptedCodexGuide(
	ctx context.Context,
	task *activeAgentTask,
	reply platform.Replier,
	messageKey string,
	localProgress string,
) codexGuideDeliveryResult {
	result := codexGuideDeliveryResult{ReplyText: codexGuideAcceptedReply}
	if task == nil {
		return result
	}

	trace := task.traceSnapshot()
	h.recordTraceStage(trace, "guide.accepted", "accepted", "input steered to active Codex turn")
	task.recordLocalProgressText(time.Now(), localProgress)
	progress, snapshot, ok := task.progressReanchorSnapshot()
	if !ok || !progress.usesNativeProgressCard() {
		return result
	}

	task.mu.Lock()
	taskID := task.taskID
	task.mu.Unlock()
	messageKey = strings.TrimSpace(messageKey)
	if messageKey == "" {
		messageKey = "client:" + uuid.NewString()
	}
	h.recordTraceStage(trace, "task.card_reanchor_started", "running", "moving progress card to accepted guide")
	move, err := progress.reanchor(ctx, reply, snapshot, codexGuideTransitionID(taskID, messageKey))
	if move.Moved {
		task.mu.Lock()
		task.trace = traceWithReply(task.trace, progressReplier(reply))
		trace = task.trace
		task.mu.Unlock()
		result.Reanchored = true
		h.recordTraceStage(trace, "task.card_reanchored", "running", "progress card moved to accepted guide")
		if move.SupersedePending {
			h.recordTraceStage(trace, "task.card_supersede_pending", "pending", "previous progress card supersede queued")
		}
	}
	if err != nil {
		h.recordTraceStage(trace, "task.card_reanchor_failed", "failed", sanitizeAgentError(err.Error()))
		result.ReplyText = fmt.Sprintf("引导已送达，但任务卡迁移失败: %s", sanitizeAgentError(err.Error()))
		return result
	}
	if move.Moved {
		result.ReplyText = ""
	}
	return result
}
