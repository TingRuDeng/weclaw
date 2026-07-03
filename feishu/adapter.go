package feishu

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

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

const approvalActionResultTimeout = 200 * time.Millisecond

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
	approvalMu sync.Mutex
	approvals  map[string]approvalRecord
	taskCards  *taskCardRegistry
	now        func() time.Time
}

const feishuApprovalTTL = 30 * time.Minute

// approvalRecord 记录一次审批决策及其时间，用于幂等去重与 TTL 清理。
type approvalRecord struct {
	action parsedCardAction
	at     time.Time
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
	log.Printf("[feishu] message event parsed: user=%s message=%s attachments=%d", msg.UserID, msg.MessageID, len(msg.Attachments))
	dispatch(ctx, msg, newReplierWithTaskCards(a.sender, firstNonEmpty(msg.ChatID, msg.UserID), a.cardKit, a.taskCards))
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
	if action.Kind == cardKindApproval {
		return a.handleApprovalCardAction(ctx, action, dispatch), nil
	}
	metadata := map[string]string{"source": "card.action.trigger"}
	if action.SessionKey != "" {
		metadata[feishuSessionMetadataKey] = action.SessionKey
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
		Metadata: metadata,
	}
	go dispatch(context.WithoutCancel(ctx), msg, newReplierWithTaskCards(a.sender, action.UserID, a.cardKit, a.taskCards))
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: "已收到"},
	}, nil
}

// handleApprovalCardAction 处理飞书审批按钮，先收纳审批卡片，再把首个 decision 交给 agent。
func (a *Adapter) handleApprovalCardAction(ctx context.Context, action parsedCardAction, dispatch platform.DispatchFunc) *callback.CardActionTriggerResponse {
	handled, first := a.recordApprovalAction(action)
	if first {
		resultCh := make(chan platform.CardActionResult, 1)
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
			Metadata: map[string]string{"source": "card.action.trigger"},
		}
		dispatch(context.WithoutCancel(ctx), msg, newReplierWithTaskCards(a.sender, handled.UserID, a.cardKit, a.taskCards))
		if a.approvalActionExpired(ctx, resultCh) {
			handled.Status = approvalStatusExpired
			a.updateApprovalActionRecord(handled)
		} else if a.updateTaskCardWithApproval(ctx, handled) {
			handled.Status = approvalStatusArchived
			a.updateApprovalActionRecord(handled)
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
	opts, ok := a.taskCards.addApproval(action.TaskCard, action)
	if !ok {
		return false
	}
	cardJSON, err := buildCardV2(opts)
	if err != nil {
		log.Printf("[feishu] failed to build task card approval snapshot: %v", err)
		return false
	}
	if err := a.cardKit.UpdateCard(ctx, action.TaskCard, cardJSON, cardKitSequence(a.nowOrDefault())); err != nil {
		log.Printf("[feishu] ignored task approval card update error: %v", err)
		return false
	}
	return true
}

// cardKitSequence 使用秒级时间生成飞书 CardKit 可接受的更新序号，避免毫秒时间戳越界。
func cardKitSequence(now time.Time) int {
	return int(now.Unix())
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
