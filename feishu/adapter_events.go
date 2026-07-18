package feishu

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	feishuCardActionTimeout          = 2 * time.Minute
	feishuInlineCardActionTimeout    = 1500 * time.Millisecond
	feishuCardActionNoticeTimeout    = 10 * time.Second
	feishuMessageDispatchWaitTimeout = 30 * time.Second
	feishuMessageDispatchNoticeDelay = 3 * time.Second
	feishuMessageNoticeTimeout       = 10 * time.Second
)

type reservedResourceRequest struct {
	ctx         context.Context
	message     *platform.IncomingMessage
	resources   []types.Resource
	reservation feishuDedupReservation
}

// newEventDispatcher 注册飞书消息事件和卡片回调事件。
func (a *Adapter) newEventDispatcher(dispatch platform.DispatchFunc) *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher("", "").
		OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
			return a.handleMessageReadEvent(ctx, event)
		}).
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			return a.handleMessageEvent(ctx, event, dispatch)
		}).
		OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			return a.handleCardActionEvent(ctx, event, dispatch)
		})
}

// handleMessageReadEvent 消化机器人消息已读事件；它只表示客户端阅读状态，不应进入业务消息流。
func (a *Adapter) handleMessageReadEvent(_ context.Context, _ *larkim.P2MessageReadV1) error {
	return nil
}

// handleMessageEvent 解析飞书消息并分发到业务层。
func (a *Adapter) handleMessageEvent(ctx context.Context, event *larkim.P2MessageReceiveV1, dispatch platform.DispatchFunc) error {
	if a.shouldIgnoreStaleMessage(event) {
		return nil
	}
	msg, resources, reservation, ok := a.toIncomingEnvelopeFromMessage(event)
	if !ok {
		log.Printf("[feishu] ignored non-dispatchable message event")
		return nil
	}
	allowed := a.allowIncomingMessage(msg)
	if allowed {
		a.rememberUserIdentities(msg)
		owned, err := a.attachReservedResources(reservedResourceRequest{
			ctx: ctx, message: &msg, resources: resources, reservation: reservation,
		})
		if !owned {
			newTemporaryAttachmentCleanup(msg.Attachments)()
			return a.handleDedupOwnershipLoss(ctx, msg)
		}
		if err != nil {
			newTemporaryAttachmentCleanup(msg.Attachments)()
			return a.handleResourceDownloadFailure(ctx, msg, reservation, err)
		}
	}
	if !reservation.complete() {
		newTemporaryAttachmentCleanup(msg.Attachments)()
		log.Printf("[feishu] ignored message after dedup reservation ownership changed")
		return nil
	}
	cleanup := newTemporaryAttachmentCleanup(msg.Attachments)
	cleanupOwned := true
	defer func() {
		if cleanupOwned {
			cleanup()
		}
	}()
	if allowed && incomingMessageEmpty(msg) {
		log.Printf("[feishu] ignored non-dispatchable message event")
		return nil
	}
	scope := ExtractFeishuSessionScope(event)
	if a.handleMirrorDedup(ctx, event, scope, msg, dispatch, cleanup) {
		cleanupOwned = false
		return nil
	}
	a.dispatchIncomingMessage(context.WithoutCancel(ctx), msg, dispatch)
	return nil
}

// attachReservedResources 在附件下载期间维持去重处理权。
func (a *Adapter) attachReservedResources(req reservedResourceRequest) (bool, error) {
	if len(req.resources) == 0 {
		return true, nil
	}
	if req.reservation.deduper == nil || req.reservation.owner == nil {
		return true, a.attachMessageResources(req.ctx, req.message, req.resources)
	}
	leaseCtx, lease := req.reservation.startLease(req.ctx)
	err := a.attachMessageResources(leaseCtx, req.message, req.resources)
	return lease.stop(), err
}

// handleDedupOwnershipLoss 明确告知用户原处理者已失去消息处理权。
func (a *Adapter) handleDedupOwnershipLoss(ctx context.Context, msg platform.IncomingMessage) error {
	target := firstNonEmpty(msg.ChatID, msg.UserID)
	replier := NewReplierForMessage(a.sender, target, msg.ReplyToID, a.cardKit)
	if err := replier.SendText(ctx, "消息处理状态已失效，请重新发送。"); err != nil {
		log.Printf("[feishu] failed to send dedup ownership loss notice: %v", err)
		return err
	}
	log.Printf("[feishu] message dedup reservation ownership lost during resource download")
	return nil
}

// handleResourceDownloadFailure 区分可重试与永久错误，并始终给用户明确反馈。
func (a *Adapter) handleResourceDownloadFailure(ctx context.Context, msg platform.IncomingMessage, reservation feishuDedupReservation, err error) error {
	permanent := isPermanentResourceDownloadError(err)
	notice := "附件获取失败，请稍后重试。"
	if permanent {
		notice = "附件获取失败：文件过大或资源不可用，请重新发送。"
	}
	target := firstNonEmpty(msg.ChatID, msg.UserID)
	replier := NewReplierForMessage(a.sender, target, msg.ReplyToID, a.cardKit)
	if sendErr := replier.SendText(ctx, notice); sendErr != nil {
		log.Printf("[feishu] failed to send resource download failure notice: %v", sendErr)
		reservation.release()
		return sendErr
	}
	log.Printf("[feishu] message resource download failed: %v", err)
	if permanent {
		reservation.complete()
		return nil
	}
	reservation.release()
	return err
}

func isPermanentResourceDownloadError(err error) bool {
	var permanent interface{ Permanent() bool }
	return errors.As(err, &permanent) && permanent.Permanent()
}

// handleMirrorDedup 在 adapter 层消化飞书话题“同时发送到群”的群聊镜像。
func (a *Adapter) handleMirrorDedup(ctx context.Context, event *larkim.P2MessageReceiveV1, scope FeishuSessionScope, msg platform.IncomingMessage, dispatch platform.DispatchFunc, cleanup func()) bool {
	if a.deduper == nil {
		return false
	}
	if hasThreadFields(scope) {
		a.deduper.recordThreadMirrorFingerprint(event, scope, msg.Text)
		return false
	}
	return a.deduper.deferPossibleGroupMirror(event, scope, msg.Text, func() {
		defer cleanup()
		a.dispatchIncomingMessage(context.WithoutCancel(ctx), msg, dispatch)
	}, cleanup)
}

// dispatchIncomingMessage 统一记录飞书消息解析结果并分发到业务层。
func (a *Adapter) dispatchIncomingMessage(ctx context.Context, msg platform.IncomingMessage, dispatch platform.DispatchFunc) {
	ticket := a.dispatches.reserve(feishuDispatchKey(msg))
	if !isFeishuStopMessage(msg) {
		dispatchCtx, cancel := context.WithTimeout(ctx, a.dispatchWait)
		defer cancel()
		outcome := ticket.runWithWaitNotice(dispatchCtx, dispatchWaitOptions{
			delay:  a.dispatchNoticeDelay,
			notice: func() { go a.sendQueueWaitNotice(msg) },
			dispatch: func() {
				a.dispatchMessage(ctx, msg, dispatch)
			},
		})
		if outcome == dispatchRunWaitCanceled || outcome == dispatchRunExecutionCanceled {
			go a.sendQueueTimeoutNotice(msg, outcome)
		}
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, a.dispatchWait)
	defer cancel()
	ordered := ticket.runAfterWaitTimeout(waitCtx, func() {
		a.dispatchMessage(ctx, msg, dispatch)
	})
	if !ordered {
		log.Printf("[feishu] message dispatch bypassed stalled predecessor: account=%s chat=%s message=%s", msg.AccountID, msg.ChatID, msg.MessageID)
	}
}

// sendQueueTimeoutNotice 区分尚未执行与执行未返回，避免超时后给出错误承诺。
func (a *Adapter) sendQueueTimeoutNotice(msg platform.IncomingMessage, outcome dispatchRunOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), feishuMessageNoticeTimeout)
	defer cancel()
	text := "前一项操作仍未结束，排队等待已超时，本消息未执行。请发送 /stop 或稍后重试。"
	if outcome == dispatchRunExecutionCanceled {
		text = "本消息处理超过等待上限，后台操作仍可能继续。请先检查当前状态，必要时发送 /stop。"
	}
	if err := a.newScopedReplier(msg).SendText(ctx, text); err != nil {
		log.Printf("[feishu] failed to send message dispatch timeout notice: %v", err)
	}
}

// sendQueueWaitNotice 独立反馈排队状态，避免平台事件 context 结束后提示被取消。
func (a *Adapter) sendQueueWaitNotice(msg platform.IncomingMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), feishuMessageNoticeTimeout)
	defer cancel()
	text := "前一项操作仍在处理，本消息已排队，完成后将自动执行。"
	if err := a.newScopedReplier(msg).SendText(ctx, text); err != nil {
		log.Printf("[feishu] failed to send message queue notice: %v", err)
	}
}

// dispatchMessage 统一记录飞书消息并调用平台分发函数。
func (a *Adapter) dispatchMessage(ctx context.Context, msg platform.IncomingMessage, dispatch platform.DispatchFunc) {
	log.Printf("[feishu] message event parsed: account=%s user=%s chat=%s message=%s attachments=%d", msg.AccountID, msg.UserID, msg.ChatID, msg.MessageID, len(msg.Attachments))
	dispatch(ctx, msg, a.newScopedReplier(msg))
}

// isFeishuStopMessage 仅允许明确的停止命令越过卡住的前序票据。
func isFeishuStopMessage(msg platform.IncomingMessage) bool {
	if strings.TrimSpace(msg.Text) == "/stop" {
		return true
	}
	return msg.RawCommand != nil && strings.TrimSpace(msg.RawCommand.Value["choice"]) == "/stop"
}

func (a *Adapter) allowIncomingMessage(msg platform.IncomingMessage) bool {
	return a.allowCardActionUser(msg.UserID, msg.UserAliases)
}

// handleCardActionEvent 在 3 秒回调窗口内立即响应，再异步把按钮动作回放到统一业务层。
func (a *Adapter) handleCardActionEvent(ctx context.Context, event *callback.CardActionTriggerEvent, dispatch platform.DispatchFunc) (*callback.CardActionTriggerResponse, error) {
	action, ok := parseCardAction(event)
	if !ok {
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: "warning", Content: "无法识别该操作"},
		}, nil
	}
	if !a.allowCardActionUser(action.UserID, action.UserAliases) {
		log.Printf("[feishu] denied card action user %q on account %q", action.UserID, a.creds.AppID)
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: "warning", Content: "当前账号未授权使用 WeClaw"},
		}, nil
	}
	action.UserAliases = mergeUserAliases(action.UserID, action.UserAliases, a.identityKeysForUser(action.UserID))
	if action.Kind == cardKindApproval {
		return a.handleApprovalCardAction(ctx, action, dispatch), nil
	}
	if a.isDuplicateCardActionEvent(action) {
		log.Printf("[feishu] ignored duplicate card action event: event_id=%s", action.EventID)
		return duplicateCardActionResponse(), nil
	}
	metadata := map[string]string{"source": "card.action.trigger"}
	if action.SessionKey != "" {
		metadata[feishuSessionMetadataKey] = action.SessionKey
	}
	msg := platform.IncomingMessage{
		Platform:    platform.PlatformFeishu,
		AccountID:   a.creds.AppID,
		UserID:      action.UserID,
		UserAliases: action.UserAliases,
		ChatID:      action.ChatID,
		MessageID:   regularCardActionMessageID(action),
		RawCommand: &platform.CardAction{
			Action: action.Action,
			Value:  regularCardActionValue(action),
		},
		Metadata: metadata,
	}
	ticket := a.dispatches.reserve(feishuDispatchKey(msg))
	if isInlineCardCommand(action.Choice) {
		return a.handleInlineCardAction(ctx, msg, action, dispatch, ticket), nil
	}
	a.dispatchCardActionAsync(ctx, msg, action, dispatch, ticket, a.newScopedReplier(msg))
	return submittedCardActionResponse(action), nil
}

func (a *Adapter) dispatchCardActionAsync(ctx context.Context, msg platform.IncomingMessage, action parsedCardAction, dispatch platform.DispatchFunc, ticket feishuDispatchTicket, reply platform.Replier) {
	dispatchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cardActionTimeout)
	go func() {
		defer cancel()
		if !ticket.run(dispatchCtx, func() { dispatch(dispatchCtx, msg, reply) }) {
			log.Printf("[feishu] card action dispatch timed out: action=%s", action.Action)
			a.sendCardActionTimeoutNotice(msg)
		}
	}()
}

// duplicateCardActionResponse 不携带卡片，避免飞书重投旧事件时把终态覆盖回“处理中”。
func duplicateCardActionResponse() *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: "该操作已受理"},
	}
}

func submittedCardActionResponse(action parsedCardAction) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: "已受理，正在处理"},
		Card:  buildSubmittedChoiceCard(action),
	}
}

func (a *Adapter) isDuplicateCardActionEvent(action parsedCardAction) bool {
	key := cardActionDedupKey(action)
	if key == "" {
		return false
	}
	now := a.nowOrDefault()
	a.cardActionMu.Lock()
	defer a.cardActionMu.Unlock()
	if a.cardActionEvents == nil {
		a.cardActionEvents = make(map[string]time.Time)
	}
	for seenKey, seenAt := range a.cardActionEvents {
		if now.Sub(seenAt) > feishuEventDedupTTL {
			delete(a.cardActionEvents, seenKey)
		}
	}
	if seenAt, ok := a.cardActionEvents[key]; ok && now.Sub(seenAt) <= feishuEventDedupTTL {
		return true
	}
	a.cardActionEvents[key] = now
	return false
}

func cardActionDedupKey(action parsedCardAction) string {
	eventID := strings.TrimSpace(action.EventID)
	if eventID == "" {
		return ""
	}
	return "card-event:" + eventID
}

// sendCardActionTimeoutNotice 使用独立短 context 反馈超时，避免沿用已经取消的分发 context。
func (a *Adapter) sendCardActionTimeoutNotice(msg platform.IncomingMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), feishuCardActionNoticeTimeout)
	defer cancel()
	if err := a.newScopedReplier(msg).SendText(ctx, "卡片操作等待超时，请检查当前状态后重试。"); err != nil {
		log.Printf("[feishu] failed to send card action timeout notice: %v", err)
	}
}

// regularCardActionValue 保留普通选择卡片的业务关联字段，避免并发问答失去精确目标。
func regularCardActionValue(action parsedCardAction) map[string]string {
	value := map[string]string{"choice": action.Choice, "conv": action.Conv}
	if action.Approval != "" {
		value["approval_key"] = action.Approval
	}
	if action.TaskCard != "" {
		value["task_card_id"] = action.TaskCard
	}
	if action.AgentName != "" {
		value[modelSettingAgentKey] = action.AgentName
	}
	if action.Kind != "" {
		value[platform.ChoiceMetadataInteractionKind] = action.Kind
	}
	if action.NavigationSnapshot != "" {
		value[platform.ChoiceMetadataNavigationSnapshot] = action.NavigationSnapshot
	}
	return value
}

// regularCardActionMessageID 优先使用飞书事件 ID；缺失时以卡片 revision 区分同一按钮的后续渲染。
func regularCardActionMessageID(action parsedCardAction) string {
	base := strings.TrimSpace(action.MessageID)
	if eventID := strings.TrimSpace(action.EventID); eventID != "" {
		return base + ":card-event:" + eventID
	}
	if revision := strings.TrimSpace(action.CardRevision); revision != "" {
		return base + ":card-revision:" + revision + ":" + action.Action + ":" + action.Choice
	}
	return base + ":card:" + action.Action + ":" + action.Choice
}

func (a *Adapter) newScopedReplier(msg platform.IncomingMessage) platform.Replier {
	target := firstNonEmpty(msg.ChatID, msg.UserID)
	return newReplierWithTaskCards(a.sender, target, a.cardKit, a.taskCards)
}
