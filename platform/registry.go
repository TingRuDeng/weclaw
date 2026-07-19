package platform

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	registryInitialRestartDelay = 3 * time.Second
	registryMaxRestartDelay     = 30 * time.Second
	denyNoticeInterval          = 60 * time.Second
	denyNoticeText              = "当前账号未授权使用 WeClaw，请联系管理员配置 allowed_users。"
)

// Registry 统一管理已启用的平台实例，并在分发前执行访问控制。
type Registry struct {
	entries            []RegistryEntry
	denyNotices        *denyNoticeLimiter
	identityObserver   IdentityObserver
	denyNoticeProvider DenyNoticeProvider
}

// RegistryEntry 描述一个平台实例及其访问控制策略。
type RegistryEntry struct {
	Platform Platform
	Access   AccessControl
}

// IdentityObserver 在访问控制前观察入站身份，只用于发现身份，不决定授权。
type IdentityObserver func(IncomingMessage)

// DenyNoticeProvider 按入站消息生成未授权提示，空返回值使用默认提示。
type DenyNoticeProvider func(IncomingMessage) string

type RegistryOption func(*Registry)

// WithIdentityObserver 注册入站身份观察器，便于拒绝未授权消息前记录飞书身份。
func WithIdentityObserver(observer IdentityObserver) RegistryOption {
	return func(r *Registry) {
		r.identityObserver = observer
	}
}

// WithDenyNoticeProvider 注册未授权提示生成器，用于附加授权码等上下文。
func WithDenyNoticeProvider(provider DenyNoticeProvider) RegistryOption {
	return func(r *Registry) {
		r.denyNoticeProvider = provider
	}
}

// NewRegistry 创建平台注册表，空白名单默认拒绝所有入站消息。
func NewRegistry(entries []RegistryEntry, opts ...RegistryOption) *Registry {
	copied := make([]RegistryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Platform == nil {
			continue
		}
		if len(entry.Access.AllowedUsers()) == 0 {
			log.Printf("[platform] WARNING: %s/%s has empty allowlist; all incoming users are denied",
				entry.Platform.Name(), entry.Platform.AccountID())
		}
		applyPlatformAccess(entry.Platform, entry.Access)
		copied = append(copied, entry)
	}
	registry := &Registry{entries: copied, denyNotices: newDenyNoticeLimiter(denyNoticeInterval)}
	for _, opt := range opts {
		if opt != nil {
			opt(registry)
		}
	}
	return registry
}

// Run 并发运行所有平台，任一平台返回错误时等待其它平台随 ctx 收敛后返回该错误。
func (r *Registry) Run(ctx context.Context, dispatch DispatchFunc) error {
	if r == nil || len(r.entries) == 0 {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, len(r.entries))
	var wg sync.WaitGroup
	for _, entry := range r.entries {
		entry := entry
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runPlatformWithRestart(runCtx, entry, dispatch, r.denyNotices, r.identityObserver, r.denyNoticeProvider); err != nil && runCtx.Err() == nil {
				errCh <- err
				cancel()
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

// ReplierFor 按平台、账号和会话 ID 查找主动发送回复器。
func (r *Registry) ReplierFor(platformName PlatformName, accountID string, chatID string) (Replier, bool) {
	return r.ReplierForRoute(DeliveryRoute{Platform: platformName, AccountID: accountID, ChatID: chatID})
}

// ReplierForRoute 按完整持久化路由重建回复器。
func (r *Registry) ReplierForRoute(route DeliveryRoute) (Replier, bool) {
	platformName := route.Platform
	accountID := route.AccountID
	chatID := route.ChatID
	if r == nil || chatID == "" {
		return nil, false
	}
	for _, entry := range r.entries {
		if platformName != "" && entry.Platform.Name() != platformName {
			continue
		}
		if accountID != "" && entry.Platform.AccountID() != accountID {
			continue
		}
		if factory, ok := entry.Platform.(OutboundRouteReplierFactory); ok {
			return factory.NewReplierForRoute(route), true
		}
		if factory, ok := entry.Platform.(OutboundReplierFactory); ok {
			return factory.NewReplier(chatID), true
		}
	}
	return nil, false
}

// OutboundAccountCount 返回指定平台可主动发送的账号数量，用于拒绝多账号歧义发送。
func (r *Registry) OutboundAccountCount(platformName PlatformName) int {
	if r == nil {
		return 0
	}
	count := 0
	for _, entry := range r.entries {
		if platformName != "" && entry.Platform.Name() != platformName {
			continue
		}
		if _, ok := entry.Platform.(OutboundReplierFactory); ok {
			count++
		}
	}
	return count
}

// HasAccount 判断指定平台账号是否已经在当前 registry 中运行。
func (r *Registry) HasAccount(platformName PlatformName, accountID string) bool {
	if r == nil {
		return false
	}
	for _, entry := range r.entries {
		if entry.Platform.Name() != platformName {
			continue
		}
		if entry.Platform.AccountID() == accountID {
			return true
		}
	}
	return false
}

// UpdateAccess 热更新指定平台的访问控制白名单，不重启平台连接。
func (r *Registry) UpdateAccess(platformName PlatformName, allowed []string) {
	if r == nil {
		return
	}
	for _, entry := range r.entries {
		if entry.Platform.Name() == platformName {
			entry.Access.SetAllowed(allowed)
			applyPlatformAccess(entry.Platform, entry.Access)
		}
	}
}

// UpdateAccessForAccount 热更新指定平台账号的访问控制白名单，不影响同平台其它账号。
func (r *Registry) UpdateAccessForAccount(platformName PlatformName, accountID string, allowed []string) {
	if r == nil {
		return
	}
	for _, entry := range r.entries {
		if entry.Platform.Name() != platformName || entry.Platform.AccountID() != accountID {
			continue
		}
		entry.Access.SetAllowed(allowed)
		applyPlatformAccess(entry.Platform, entry.Access)
	}
}

func applyPlatformAccess(p Platform, access AccessControl) {
	if target, ok := p.(AccessControlledPlatform); ok {
		target.SetAccessControl(access)
	}
}

func runPlatformWithRestart(ctx context.Context, entry RegistryEntry, dispatch DispatchFunc, limiter *denyNoticeLimiter, observer IdentityObserver, denyProvider DenyNoticeProvider) error {
	restartDelay := registryInitialRestartDelay
	for {
		err := entry.Platform.Run(ctx, guardedDispatch(entry, dispatch, limiter, observer, denyProvider))
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			return nil
		}
		log.Printf("[platform] %s/%s stopped: %v; restarting in %s",
			entry.Platform.Name(), entry.Platform.AccountID(), err, restartDelay)
		select {
		case <-time.After(restartDelay):
		case <-ctx.Done():
			return nil
		}
		restartDelay *= 2
		if restartDelay > registryMaxRestartDelay {
			restartDelay = registryMaxRestartDelay
		}
	}
}

func guardedDispatch(entry RegistryEntry, dispatch DispatchFunc, limiter *denyNoticeLimiter, observer IdentityObserver, denyProvider DenyNoticeProvider) DispatchFunc {
	return func(ctx context.Context, msg IncomingMessage, reply Replier) {
		observeIncomingIdentity(observer, msg)
		if !accessAllowsMessage(entry.Access, msg) {
			log.Printf("[platform] denied %s user %q aliases=%q on account %q",
				entry.Platform.Name(), msg.UserID, msg.UserAliases, entry.Platform.AccountID())
			sendDenyNotice(ctx, entry, msg, reply, limiter, denyProvider)
			return
		}
		dispatch(ctx, msg, reply)
	}
}

func observeIncomingIdentity(observer IdentityObserver, msg IncomingMessage) {
	if observer == nil || msg.Platform != PlatformFeishu {
		return
	}
	observer(msg)
}

// accessAllowsMessage 用主用户 ID 和平台身份别名共同判断授权。
func accessAllowsMessage(access AccessControl, msg IncomingMessage) bool {
	for _, userID := range msg.UserIdentityKeys() {
		if access.Allowed(userID) {
			return true
		}
	}
	return false
}
