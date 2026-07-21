package feishu

import (
	"context"
	"encoding/json"
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
	action.Status = approvalStatusPending
	handled, first := a.recordApprovalAction(action)
	if first {
		resultCh := make(chan platform.CardActionResult, 1)
		msg := a.approvalActionMessage(handled, resultCh)
		resolved := make(chan parsedCardAction, 1)
		dispatchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cardActionTimeout)
		go func() {
			defer cancel()
			resolved <- a.resolveApprovalCardAction(dispatchCtx, handled, msg, resultCh, dispatch)
		}()
		timer := time.NewTimer(approvalActionResultTimeout)
		defer timer.Stop()
		select {
		case handled = <-resolved:
		case <-timer.C:
			go a.patchResolvedApprovalCard(resolved)
		case <-ctx.Done():
			go a.patchResolvedApprovalCard(resolved)
		}
	}
	return a.approvalCardActionResponse(handled)
}

func (a *Adapter) approvalActionMessage(action parsedCardAction, resultCh chan platform.CardActionResult) platform.IncomingMessage {
	metadata := map[string]string{"source": "card.action.trigger"}
	if action.SessionKey != "" {
		metadata[feishuSessionMetadataKey] = action.SessionKey
	}
	return platform.IncomingMessage{
		Platform:    platform.PlatformFeishu,
		AccountID:   a.creds.AppID,
		UserID:      action.UserID,
		UserAliases: action.UserAliases,
		ChatID:      action.ChatID,
		MessageID:   action.MessageID + ":card:" + action.Action + ":" + action.Choice,
		RawCommand: &platform.CardAction{
			Action: action.Action,
			Value: map[string]string{
				"choice":                               action.Choice,
				"conv":                                 action.Conv,
				"approval_key":                         action.Approval,
				"task_card_id":                         action.TaskCard,
				platform.ChoiceMetadataInteractionKind: platform.ChoiceInteractionApproval,
			},
			Result: resultCh,
		},
		Metadata: metadata,
	}
}

func (a *Adapter) resolveApprovalCardAction(ctx context.Context, action parsedCardAction, msg platform.IncomingMessage, resultCh <-chan platform.CardActionResult, dispatch platform.DispatchFunc) parsedCardAction {
	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		dispatch(ctx, msg, newReplierWithTaskCards(a.sender, action.UserID, a.cardKit, a.taskCards).withDeliveryAccount(a.creds.AppID))
	}()

	result, confirmed := waitForApprovalActionResult(ctx, resultCh, dispatchDone)
	switch {
	case !confirmed:
		action.Status = approvalStatusUnconfirmed
	case result == platform.CardActionResultExpired:
		action.Status = approvalStatusExpired
	case result == platform.CardActionResultConsumed && a.updateTaskCardWithApproval(ctx, action):
		action.Status = approvalStatusArchived
	case result == platform.CardActionResultConsumed:
		action.Status = approvalStatusHandled
	default:
		action.Status = approvalStatusUnconfirmed
	}
	a.updateApprovalActionRecord(action)
	return action
}

func waitForApprovalActionResult(ctx context.Context, resultCh <-chan platform.CardActionResult, dispatchDone <-chan struct{}) (platform.CardActionResult, bool) {
	select {
	case result := <-resultCh:
		return result, true
	case <-dispatchDone:
		select {
		case result := <-resultCh:
			return result, true
		default:
			return "", false
		}
	case <-ctx.Done():
		return "", false
	}
}

func (a *Adapter) patchResolvedApprovalCard(resolved <-chan parsedCardAction) {
	action := <-resolved
	response := a.approvalCardActionResponse(action)
	if response == nil || response.Card == nil || a.sender == nil || strings.TrimSpace(action.MessageID) == "" {
		return
	}
	cardJSON, err := json.Marshal(response.Card.Data)
	if err != nil {
		log.Printf("[feishu] failed to marshal resolved approval card: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), feishuCardActionNoticeTimeout)
	defer cancel()
	if err := a.sender.PatchCard(ctx, action.MessageID, string(cardJSON)); err != nil {
		log.Printf("[feishu] failed to patch resolved approval card: %v", err)
	}
}

func (a *Adapter) approvalCardActionResponse(handled parsedCardAction) *callback.CardActionTriggerResponse {
	if strings.TrimSpace(handled.Status) == approvalStatusPending {
		return &callback.CardActionTriggerResponse{
			Toast: approvalActionToast(handled),
			Card:  buildSubmittedChoiceCard(handled),
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
	switch strings.TrimSpace(action.Status) {
	case approvalStatusPending:
		return &callback.Toast{Type: "info", Content: "已受理，正在处理"}
	case approvalStatusExpired:
		return &callback.Toast{Type: "warning", Content: "授权请求已过期，请重新发起任务"}
	case approvalStatusUnconfirmed:
		return &callback.Toast{Type: "warning", Content: "授权处理结果未确认，请重新发起任务"}
	default:
		return &callback.Toast{Type: "success", Content: "已处理"}
	}
}

// approvalActionOwnedByUser 在写入幂等记录前拦截非发起人，避免群聊里抢先点击耗掉审批。
func approvalActionOwnedByUser(action parsedCardAction) bool {
	owner := strings.TrimSpace(action.Owner)
	if owner == "" {
		return false
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
		if messageID := strings.TrimSpace(action.MessageID); messageID != "" {
			return "approval\x00" + key + "\x00message\x00" + messageID
		}
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
