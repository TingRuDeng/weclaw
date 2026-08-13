package feishu

import (
	"context"
	"log"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const (
	cardActionTaskProgressExpand   = "task_progress_expand"
	cardActionTaskProgressCollapse = "task_progress_collapse"
)

func (a *Adapter) handleTaskProgressControl(ctx context.Context, action parsedCardAction, expanded bool) *callback.CardActionTriggerResponse {
	cardID := strings.TrimSpace(action.TaskCard)
	if a.taskCards == nil || a.cardKit == nil || cardID == "" {
		return taskProgressControlWarning("任务卡状态已失效，请重新打开最新任务卡")
	}
	opts, sequence, previousExpanded, ok := a.taskCards.setExpandedWithSequence(cardID, expanded)
	if !ok {
		return taskProgressControlWarning("任务卡状态已失效，请重新打开最新任务卡")
	}
	cardJSON, err := buildCardV2(opts)
	if err != nil {
		a.taskCards.restoreExpandedIfSequence(cardID, sequence, previousExpanded)
		log.Printf("[feishu] failed to build task progress visibility update: expanded=%t err=%v", expanded, err)
		return taskProgressControlWarning("更新完整进度显示失败，请重试")
	}
	if err := a.cardKit.UpdateCard(ctx, cardID, cardJSON, sequence); err != nil {
		a.taskCards.restoreExpandedIfSequence(cardID, sequence, previousExpanded)
		log.Printf("[feishu] failed to update task progress visibility: card=%q expanded=%t err=%v", cardID, expanded, err)
		return taskProgressControlWarning("更新完整进度显示失败，请重试")
	}
	a.taskCards.notifyDurableReferenceChange(cardID)
	toast := "已收起完整进度"
	if expanded {
		toast = "已展开完整进度"
	}
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: toast},
	}
}

func taskProgressControlWarning(content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "warning", Content: content},
	}
}
