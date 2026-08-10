package feishu

import (
	"context"
	"log"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const cardActionTaskProgressCollapse = "task_progress_collapse"

func (a *Adapter) handleTaskProgressCollapse(ctx context.Context, action parsedCardAction) *callback.CardActionTriggerResponse {
	cardID := strings.TrimSpace(action.TaskCard)
	if a.taskCards == nil || a.cardKit == nil || cardID == "" {
		return taskProgressControlWarning("任务卡状态已失效，请使用卡片顶部的完整进度控件")
	}
	opts, sequence, ok := a.taskCards.setExpandedWithSequence(cardID, false)
	if !ok {
		return taskProgressControlWarning("任务卡状态已失效，请使用卡片顶部的完整进度控件")
	}
	cardJSON, err := buildCardV2(opts)
	if err != nil {
		log.Printf("[feishu] failed to build collapsed task progress card: %v", err)
		return taskProgressControlWarning("收起完整进度失败，请重试")
	}
	if err := a.cardKit.UpdateCard(ctx, cardID, cardJSON, sequence); err != nil {
		log.Printf("[feishu] failed to collapse task progress card %q: %v", cardID, err)
		return taskProgressControlWarning("收起完整进度失败，请重试")
	}
	a.taskCards.notifyDurableReferenceChange(cardID)
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: "已收起完整进度"},
	}
}

func taskProgressControlWarning(content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "warning", Content: content},
	}
}
