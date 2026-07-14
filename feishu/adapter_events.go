package feishu

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const feishuCardActionTimeout = 2 * time.Minute

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
		if err := a.attachMessageResources(ctx, &msg, resources); err != nil {
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
	ticket.run(ctx, func() {
		log.Printf("[feishu] message event parsed: account=%s user=%s chat=%s message=%s attachments=%d", msg.AccountID, msg.UserID, msg.ChatID, msg.MessageID, len(msg.Attachments))
		dispatch(ctx, msg, a.newScopedReplier(msg))
	})
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
		MessageID:   action.MessageID + ":card:" + action.Action + ":" + action.Choice,
		RawCommand: &platform.CardAction{
			Action: action.Action,
			Value:  regularCardActionValue(action),
		},
		Metadata: metadata,
	}
	ticket := a.dispatches.reserve(feishuDispatchKey(msg))
	dispatchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), feishuCardActionTimeout)
	go func() {
		defer cancel()
		if !ticket.run(dispatchCtx, func() { dispatch(dispatchCtx, msg, a.newScopedReplier(msg)) }) {
			log.Printf("[feishu] card action dispatch timed out: action=%s", action.Action)
		}
	}()
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: "已提交，正在处理"},
		Card:  buildSubmittedChoiceCard(action),
	}, nil
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
	return value
}

func (a *Adapter) newScopedReplier(msg platform.IncomingMessage) platform.Replier {
	target := firstNonEmpty(msg.ChatID, msg.UserID)
	return newReplierWithTaskCards(a.sender, target, a.cardKit, a.taskCards)
}
