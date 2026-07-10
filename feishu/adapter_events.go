package feishu

import (
	"context"
	"log"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

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
	msg, resources, ok := a.toIncomingEnvelopeFromMessage(event)
	if !ok {
		log.Printf("[feishu] ignored non-dispatchable message event")
		return nil
	}
	a.rememberUserIdentities(msg)
	if a.allowIncomingMessage(msg) {
		if err := a.attachMessageResources(ctx, &msg, resources); err != nil {
			log.Printf("[feishu] ignored message with resource download failure: %v", err)
			return nil
		}
		if incomingMessageEmpty(msg) {
			log.Printf("[feishu] ignored non-dispatchable message event")
			return nil
		}
	}
	scope := ExtractFeishuSessionScope(event)
	if a.handleMirrorDedup(ctx, event, scope, msg, dispatch) {
		return nil
	}
	a.dispatchIncomingMessage(ctx, msg, dispatch)
	return nil
}

// handleMirrorDedup 在 adapter 层消化飞书话题“同时发送到群”的群聊镜像。
func (a *Adapter) handleMirrorDedup(ctx context.Context, event *larkim.P2MessageReceiveV1, scope FeishuSessionScope, msg platform.IncomingMessage, dispatch platform.DispatchFunc) bool {
	if a.deduper == nil {
		return false
	}
	if hasThreadFields(scope) {
		a.deduper.recordThreadMirrorFingerprint(event, scope, msg.Text)
		return false
	}
	return a.deduper.deferPossibleGroupMirror(event, scope, msg.Text, func() {
		a.dispatchIncomingMessage(context.WithoutCancel(ctx), msg, dispatch)
	})
}

// dispatchIncomingMessage 统一记录飞书消息解析结果并分发到业务层。
func (a *Adapter) dispatchIncomingMessage(ctx context.Context, msg platform.IncomingMessage, dispatch platform.DispatchFunc) {
	log.Printf("[feishu] message event parsed: account=%s user=%s chat=%s message=%s attachments=%d", msg.AccountID, msg.UserID, msg.ChatID, msg.MessageID, len(msg.Attachments))
	dispatch(ctx, msg, a.newScopedReplier(msg))
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
			Value: map[string]string{
				"choice": action.Choice,
				"conv":   action.Conv,
			},
		},
		Metadata: metadata,
	}
	go dispatch(context.WithoutCancel(ctx), msg, a.newScopedReplier(msg))
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: "已收到"},
		Card:  buildSelectedChoiceCard(action),
	}, nil
}

func (a *Adapter) newScopedReplier(msg platform.IncomingMessage) platform.Replier {
	target := firstNonEmpty(msg.ChatID, msg.UserID)
	return newReplierWithTaskCards(a.sender, target, a.cardKit, a.taskCards)
}
