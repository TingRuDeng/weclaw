package wechat

import (
	"context"
	"log"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
)

const (
	wechatWatchdogInterval = 60 * time.Second
	wechatWatchdogMaxIdle  = 5 * time.Minute
)

type monitorRunner interface {
	Run(ctx context.Context) error
	LastActivity() time.Time
	SetAggregationWindow(window time.Duration)
}

// Adapter 将现有 iLink 微信传输封装为统一平台接口。
type Adapter struct {
	creds             *ilink.Credentials
	client            *ilink.Client
	newMonitor        func(*ilink.Client, ilink.MessageHandler) (monitorRunner, error)
	watchdogInterval  time.Duration
	watchdogMaxIdle   time.Duration
	aggregationWindow time.Duration
	tokenStore        *contextTokenStore
}

// NewAdapter 使用微信凭证创建 adapter。
func NewAdapter(creds *ilink.Credentials) *Adapter {
	return &Adapter{
		creds:             creds,
		client:            ilink.NewClient(creds),
		newMonitor:        newIlinkMonitor,
		watchdogInterval:  wechatWatchdogInterval,
		watchdogMaxIdle:   wechatWatchdogMaxIdle,
		aggregationWindow: 800 * time.Millisecond,
		tokenStore:        newContextTokenStore(creds.ILinkBotID),
	}
}

func (a *Adapter) Name() platform.PlatformName {
	return platform.PlatformWeChat
}

func (a *Adapter) AccountID() string {
	if a == nil || a.creds == nil {
		return ""
	}
	return a.creds.ILinkBotID
}

func (a *Adapter) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		Text:     true,
		Typing:   true,
		Image:    true,
		File:     true,
		LongText: true,
	}
}

// NewReplier 为主动发送 API 创建微信回复器。
func (a *Adapter) NewReplier(chatID string) platform.Replier {
	contextToken := ""
	if a != nil && a.tokenStore != nil {
		contextToken = a.tokenStore.Get(chatID)
	}
	return NewReplier(a.client, chatID, contextToken, "")
}

// NewReplierForRoute 复用持久化 context_token 恢复终态投递。
func (a *Adapter) NewReplierForRoute(route platform.DeliveryRoute) platform.Replier {
	return a.NewReplier(route.ChatID)
}

// Run 启动微信长轮询；入站消息转换将在后续任务接入。
func (a *Adapter) Run(ctx context.Context, dispatch platform.DispatchFunc) error {
	monitor, err := a.newMonitor(a.client, a.handleWeixinMessage(dispatch))
	if err != nil {
		return err
	}
	monitor.SetAggregationWindow(a.aggregationWindow)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.runWatchdog(runCtx, monitor, cancel)
	err = monitor.Run(runCtx)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// SetAggregationWindow 设置微信同一用户连续消息聚合窗口；0 表示关闭聚合。
func (a *Adapter) SetAggregationWindow(window time.Duration) {
	a.aggregationWindow = window
}

func (a *Adapter) handleWeixinMessage(dispatch platform.DispatchFunc) ilink.MessageHandler {
	return func(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
		if !ShouldDispatchWeixinMessage(client.BotID(), msg) {
			return
		}
		if a.tokenStore != nil {
			if err := a.tokenStore.Set(msg.FromUserID, msg.ContextToken); err != nil {
				log.Printf("[wechat] failed to persist context_token for %s: %v", msg.FromUserID, err)
			}
		}
		reply := NewReplier(client, msg.FromUserID, msg.ContextToken, "")
		dispatch(ctx, IncomingFromWeixin(msg), reply)
	}
}

func newIlinkMonitor(client *ilink.Client, handler ilink.MessageHandler) (monitorRunner, error) {
	return ilink.NewMonitor(client, handler)
}

func (a *Adapter) runWatchdog(ctx context.Context, monitor monitorRunner, cancel context.CancelFunc) {
	interval := a.watchdogInterval
	if interval <= 0 {
		interval = wechatWatchdogInterval
	}
	maxIdle := a.watchdogMaxIdle
	if maxIdle <= 0 {
		maxIdle = wechatWatchdogMaxIdle
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			idle := time.Since(monitor.LastActivity())
			if idle <= maxIdle {
				continue
			}
			log.Printf("[wechat] watchdog restarting stalled monitor: idle=%s max_idle=%s", idle.Round(time.Second), maxIdle)
			cancel()
			return
		}
	}
}
