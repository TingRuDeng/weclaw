package feishu

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
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
	deduper    *feishuEventDeduper
	accessMu   sync.RWMutex
	access     platform.AccessControl
	accessSet  bool
	identityMu sync.RWMutex
	identities map[string][]string
	approvalMu sync.Mutex
	approvals  map[string]approvalRecord
	taskCards  *taskCardRegistry
	now        func() time.Time
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
		deduper:    newFeishuEventDeduper(feishuEventDedupTTL),
		identities: make(map[string][]string),
		approvals:  make(map[string]approvalRecord),
		taskCards:  newTaskCardRegistry(),
		now:        time.Now,
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

// SetSessionOptions 设置飞书群聊触发策略。
func (a *Adapter) SetSessionOptions(options FeishuSessionOptions) {
	a.session = options
}

// SetDedupStateFile 设置飞书消息事件短期去重状态文件。
func (a *Adapter) SetDedupStateFile(path string) {
	if a.deduper == nil {
		return
	}
	a.deduper.setStateFile(path)
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
	return newReplierWithTaskCards(a.sender, chatID, a.cardKit, a.taskCards)
}

// Run 校验凭证并启动飞书长连接，收到事件后交给 dispatcher 处理。
func (a *Adapter) Run(ctx context.Context, dispatch platform.DispatchFunc) error {
	if err := a.validate(ctx, a.creds); err != nil {
		return err
	}
	logPermissionGuide(a.creds.AppID)
	eventDispatcher := a.newEventDispatcher(dispatch)
	wsClient := a.wsFactory(eventDispatcher)
	errCh := make(chan error, 1)
	go func() {
		errCh <- wsClient.Start(ctx)
	}()
	select {
	case err := <-errCh:
		if ctx.Err() != nil {
			return nil
		}
		return err
	case <-ctx.Done():
		wsClient.Close()
		return nil
	}
}

func (a *Adapter) allowCardActionUser(userID string, aliases []string) bool {
	a.accessMu.RLock()
	access := a.access
	accessSet := a.accessSet
	a.accessMu.RUnlock()
	if !accessSet {
		return true
	}
	identities := mergeUserAliases(userID, aliases, a.identityKeysForUser(userID))
	for _, identity := range identities {
		if access.Allowed(identity) {
			return true
		}
	}
	return false
}

// rememberUserIdentities 缓存飞书用户身份，供后续卡片回调复用 union_id。
func (a *Adapter) rememberUserIdentities(msg platform.IncomingMessage) {
	keys := msg.UserIdentityKeys()
	if len(keys) == 0 {
		return
	}
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	for _, key := range keys {
		a.identities[key] = append([]string(nil), keys...)
	}
}

// identityKeysForUser 返回某个飞书回调用户可用于授权的所有已知身份。
func (a *Adapter) identityKeysForUser(userID string) []string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	a.identityMu.RLock()
	cached := append([]string(nil), a.identities[userID]...)
	a.identityMu.RUnlock()
	if len(cached) > 0 {
		return cached
	}
	return []string{userID}
}

// mergeUserAliases 合并多来源身份，保留主 ID 以便后续统一去重。
func mergeUserAliases(userID string, groups ...[]string) []string {
	msg := platform.IncomingMessage{UserID: userID}
	for _, group := range groups {
		msg.UserAliases = append(msg.UserAliases, group...)
	}
	return msg.UserIdentityKeys()
}
