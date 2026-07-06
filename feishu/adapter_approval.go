package feishu

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const (
	approvalActionResultTimeout = 200 * time.Millisecond
	feishuApprovalTTL           = 30 * time.Minute
)

// approvalRecord 记录一次审批决策及其时间，用于幂等去重与 TTL 清理。
type approvalRecord struct {
	action parsedCardAction
	at     time.Time
}

// handleApprovalCardAction 处理飞书审批按钮，先收纳审批卡片，再把首个 decision 交给 agent。
func (a *Adapter) handleApprovalCardAction(ctx context.Context, action parsedCardAction, dispatch platform.DispatchFunc) *callback.CardActionTriggerResponse {
	if !approvalActionOwnedByUser(action) {
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: "warning", Content: "只有任务发起人可以审批"},
		}
	}
	handled, first := a.recordApprovalAction(action)
	if first {
		resultCh := make(chan platform.CardActionResult, 1)
		metadata := map[string]string{"source": "card.action.trigger"}
		if handled.SessionKey != "" {
			metadata[feishuSessionMetadataKey] = handled.SessionKey
		}
		msg := platform.IncomingMessage{
			Platform:  platform.PlatformFeishu,
			AccountID: a.creds.AppID,
			UserID:    handled.UserID,
			ChatID:    handled.ChatID,
			MessageID: handled.MessageID + ":card:" + handled.Action + ":" + handled.Choice,
			RawCommand: &platform.CardAction{
				Action: handled.Action,
				Value: map[string]string{
					"choice":       handled.Choice,
					"conv":         handled.Conv,
					"approval_key": handled.Approval,
					"task_card_id": handled.TaskCard,
				},
				Result: resultCh,
			},
			Metadata: metadata,
		}
		dispatch(context.WithoutCancel(ctx), msg, newReplierWithTaskCards(a.sender, handled.UserID, a.cardKit, a.taskCards))
		if a.approvalActionExpired(ctx, resultCh) {
			handled.Status = approvalStatusExpired
			a.updateApprovalActionRecord(handled)
		} else if a.updateTaskCardWithApproval(ctx, handled) {
			handled.Status = approvalStatusArchived
			a.updateApprovalActionRecord(handled)
		} else {
			handled.Status = approvalStatusHandled
			a.updateApprovalActionRecord(handled)
		}
	}
	if panelCard := a.updateApprovalPanelWithAction(handled); panelCard != nil {
		return &callback.CardActionTriggerResponse{
			Toast: approvalActionToast(handled),
			Card:  panelCard,
		}
	}
	return &callback.CardActionTriggerResponse{
		Toast: approvalActionToast(handled),
		Card:  buildChoiceHandledCard(handled),
	}
}

func approvalActionToast(action parsedCardAction) *callback.Toast {
	if strings.TrimSpace(action.Status) == approvalStatusExpired {
		return &callback.Toast{Type: "warning", Content: "授权请求已过期，请重新发起任务"}
	}
	return &callback.Toast{Type: "success", Content: "已处理"}
}

func (a *Adapter) approvalActionExpired(ctx context.Context, resultCh <-chan platform.CardActionResult) bool {
	timer := time.NewTimer(approvalActionResultTimeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		return result == platform.CardActionResultExpired
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// approvalActionOwnedByUser 在写入幂等记录前拦截非发起人，避免群聊里抢先点击耗掉审批。
func approvalActionOwnedByUser(action parsedCardAction) bool {
	owner := strings.TrimSpace(action.Owner)
	if owner == "" {
		return true
	}
	return owner == strings.TrimSpace(action.UserID)
}

func (a *Adapter) recordApprovalAction(action parsedCardAction) (parsedCardAction, bool) {
	key := approvalActionKey(action)
	if key == "" {
		return action, true
	}
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	if a.approvals == nil {
		a.approvals = make(map[string]approvalRecord)
	}
	now := a.nowOrDefault()
	a.purgeApprovalsLocked(now)
	if existing, ok := a.approvals[key]; ok {
		return existing.action, false
	}
	a.approvals[key] = approvalRecord{action: action, at: now}
	return action, true
}

func (a *Adapter) updateApprovalActionRecord(action parsedCardAction) {
	key := approvalActionKey(action)
	if key == "" {
		return
	}
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	if existing, ok := a.approvals[key]; ok {
		existing.action = action
		a.approvals[key] = existing
	}
}

// purgeApprovalsLocked 清理超过 TTL 的审批记录，避免长期运行内存无限增长。
func (a *Adapter) purgeApprovalsLocked(now time.Time) {
	for key, rec := range a.approvals {
		if now.Sub(rec.at) > feishuApprovalTTL {
			delete(a.approvals, key)
		}
	}
}

func (a *Adapter) nowOrDefault() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func approvalActionKey(action parsedCardAction) string {
	if key := strings.TrimSpace(action.Approval); key != "" {
		return "approval\x00" + key
	}
	if messageID := strings.TrimSpace(action.MessageID); messageID != "" {
		return "message\x00" + messageID
	}
	return ""
}

func (a *Adapter) updateTaskCardWithApproval(ctx context.Context, action parsedCardAction) bool {
	if a.cardKit == nil || strings.TrimSpace(action.TaskCard) == "" {
		return false
	}
	opts, sequence, ok := a.taskCards.addApprovalWithSequence(action.TaskCard, action)
	if !ok {
		return false
	}
	cardJSON, err := buildCardV2(opts)
	if err != nil {
		log.Printf("[feishu] failed to build task card approval snapshot: %v", err)
		return false
	}
	if err := a.cardKit.UpdateCard(ctx, action.TaskCard, cardJSON, sequence); err != nil {
		log.Printf("[feishu] ignored task approval card update error: %v", err)
		return false
	}
	return true
}

func (a *Adapter) updateApprovalPanelWithAction(action parsedCardAction) *callback.Card {
	if !action.Panel || a.taskCards == nil {
		return nil
	}
	if strings.TrimSpace(action.Status) == approvalStatusArchived {
		snapshot, ok := a.taskCards.removeApprovalPanelItem(action.TaskCard, action.Approval)
		if !ok {
			return nil
		}
		return buildApprovalPanelCallbackCard(snapshot)
	}
	snapshot, ok := a.taskCards.completeApprovalPanelItem(action)
	if !ok {
		return nil
	}
	return buildApprovalPanelCallbackCard(snapshot)
}
