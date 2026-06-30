package feishu

import (
	"context"
	"log"
	"sync"

	"github.com/fastclaw-ai/weclaw/platform"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type wsRunner interface {
	Start(ctx context.Context) error
	Close()
}

// Adapter 将飞书长连接事件适配为平台无关消息。
type Adapter struct {
	creds      Credentials
	downloader resourceDownloader
	sender     messageSender
	cardKit    cardKitClient
	validate   func(context.Context, Credentials) error
	wsFactory  func(*dispatcher.EventDispatcher) wsRunner
	session    FeishuSessionOptions
	accessMu   sync.RWMutex
	access     platform.AccessControl
	accessSet  bool
}

// NewAdapter 创建飞书平台 adapter。
func NewAdapter(creds Credentials) *Adapter {
	restClient := lark.NewClient(creds.AppID, creds.AppSecret)
	adapter := &Adapter{
		creds:      creds,
		downloader: newSDKResourceDownloader(restClient),
		sender:     newSDKMessageSender(restClient, creds.AppID),
		cardKit:    newSDKCardKitClient(restClient, creds.AppID),
		validate:   ValidateCredentials,
		session:    DefaultFeishuSessionOptions(),
	}
	adapter.wsFactory = func(eventDispatcher *dispatcher.EventDispatcher) wsRunner {
		return larkws.NewClient(
			creds.AppID,
			creds.AppSecret,
			larkws.WithEventHandler(eventDispatcher),
		)
	}
	return adapter
}

// SetSessionOptions 设置飞书群聊触发和 thread 隔离策略。
func (a *Adapter) SetSessionOptions(options FeishuSessionOptions) {
	a.session = options
}

// Name 返回平台名称。
func (a *Adapter) Name() platform.PlatformName {
	return platform.PlatformFeishu
}

// AccountID 返回飞书应用 ID，用于区分多平台实例。
func (a *Adapter) AccountID() string {
	return a.creds.AppID
}

// Capabilities 声明飞书原生支持的回复能力。
func (a *Adapter) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		Text:      true,
		Typing:    true,
		Image:     true,
		File:      true,
		Card:      true,
		Streaming: true,
		Buttons:   true,
		LongText:  false,
	}
}

// SetAccessControl 接收 Registry 管理的访问控制器，供飞书卡片回调同步校验。
func (a *Adapter) SetAccessControl(access platform.AccessControl) {
	a.accessMu.Lock()
	a.access = access
	a.accessSet = true
	a.accessMu.Unlock()
}

// NewReplier 为主动发送 API 创建飞书回复器。
func (a *Adapter) NewReplier(chatID string) platform.Replier {
	return NewReplier(a.sender, chatID, a.cardKit)
}

// Run 校验凭证并启动飞书长连接，收到事件后交给 dispatcher 处理。
func (a *Adapter) Run(ctx context.Context, dispatch platform.DispatchFunc) error {
	if err := a.validate(ctx, a.creds); err != nil {
		return err
	}
	logPermissionGuide(a.creds.AppID)
	eventDispatcher := a.newEventDispatcher(dispatch)
	wsClient := a.wsFactory(eventDispatcher)
	go func() {
		<-ctx.Done()
		wsClient.Close()
	}()
	err := wsClient.Start(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// newEventDispatcher 注册飞书消息事件和卡片回调事件。
func (a *Adapter) newEventDispatcher(dispatch platform.DispatchFunc) *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			return a.handleMessageEvent(ctx, event, dispatch)
		}).
		OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			return a.handleCardActionEvent(ctx, event, dispatch)
		})
}

// handleMessageEvent 解析飞书消息并分发到业务层。
func (a *Adapter) handleMessageEvent(ctx context.Context, event *larkim.P2MessageReceiveV1, dispatch platform.DispatchFunc) error {
	msg, ok := a.toIncomingFromMessage(ctx, event)
	if !ok {
		log.Printf("[feishu] ignored non-dispatchable message event")
		return nil
	}
	log.Printf("[feishu] message event parsed: user=%s message=%s attachments=%d", msg.UserID, msg.MessageID, len(msg.Attachments))
	dispatch(ctx, msg, NewReplier(a.sender, firstNonEmpty(msg.ChatID, msg.UserID), a.cardKit))
	return nil
}

// handleCardActionEvent 在 3 秒回调窗口内立即响应，再异步把按钮动作回放到统一业务层。
func (a *Adapter) handleCardActionEvent(ctx context.Context, event *callback.CardActionTriggerEvent, dispatch platform.DispatchFunc) (*callback.CardActionTriggerResponse, error) {
	action, ok := parseCardAction(event)
	if !ok {
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: "warning", Content: "无法识别该操作"},
		}, nil
	}
	if !a.allowCardActionUser(action.UserID) {
		log.Printf("[feishu] denied card action user %q on account %q", action.UserID, a.creds.AppID)
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: "warning", Content: "当前账号未授权使用 WeClaw"},
		}, nil
	}
	msg := platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: a.creds.AppID,
		UserID:    action.UserID,
		ChatID:    action.ChatID,
		MessageID: action.MessageID + ":card:" + action.Action + ":" + action.Choice,
		RawCommand: &platform.CardAction{
			Action: action.Action,
			Value: map[string]string{
				"choice": action.Choice,
				"conv":   action.Conv,
			},
		},
		Metadata: map[string]string{
			"source": "card.action.trigger",
		},
	}
	go dispatch(context.WithoutCancel(ctx), msg, NewReplier(a.sender, action.UserID, a.cardKit))
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: "已收到"},
	}, nil
}

func (a *Adapter) allowCardActionUser(userID string) bool {
	a.accessMu.RLock()
	access := a.access
	accessSet := a.accessSet
	a.accessMu.RUnlock()
	if !accessSet {
		return true
	}
	return access.Allowed(userID)
}
