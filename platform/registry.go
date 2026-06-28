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
	entries     []RegistryEntry
	denyNotices *denyNoticeLimiter
}

// RegistryEntry 描述一个平台实例及其访问控制策略。
type RegistryEntry struct {
	Platform Platform
	Access   AccessControl
}

// NewRegistry 创建平台注册表，空白名单默认拒绝所有入站消息。
func NewRegistry(entries []RegistryEntry) *Registry {
	copied := make([]RegistryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Platform == nil {
			continue
		}
		if len(entry.Access.AllowedUsers()) == 0 {
			log.Printf("[platform] WARNING: %s/%s has empty allowlist; all incoming users are denied",
				entry.Platform.Name(), entry.Platform.AccountID())
		}
		copied = append(copied, entry)
	}
	return &Registry{entries: copied, denyNotices: newDenyNoticeLimiter(denyNoticeInterval)}
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
			if err := runPlatformWithRestart(runCtx, entry, dispatch, r.denyNotices); err != nil && runCtx.Err() == nil {
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
		factory, ok := entry.Platform.(OutboundReplierFactory)
		if !ok {
			continue
		}
		return factory.NewReplier(chatID), true
	}
	return nil, false
}

// UpdateAccess 热更新指定平台的访问控制白名单，不重启平台连接。
func (r *Registry) UpdateAccess(platformName PlatformName, allowed []string) {
	if r == nil {
		return
	}
	for _, entry := range r.entries {
		if entry.Platform.Name() == platformName {
			entry.Access.SetAllowed(allowed)
		}
	}
}

func runPlatformWithRestart(ctx context.Context, entry RegistryEntry, dispatch DispatchFunc, limiter *denyNoticeLimiter) error {
	restartDelay := registryInitialRestartDelay
	for {
		err := entry.Platform.Run(ctx, guardedDispatch(entry, dispatch, limiter))
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

func guardedDispatch(entry RegistryEntry, dispatch DispatchFunc, limiter *denyNoticeLimiter) DispatchFunc {
	return func(ctx context.Context, msg IncomingMessage, reply Replier) {
		if !entry.Access.Allowed(msg.UserID) {
			log.Printf("[platform] denied %s user %q on account %q", entry.Platform.Name(), msg.UserID, entry.Platform.AccountID())
			sendDenyNotice(ctx, entry, msg, reply, limiter)
			return
		}
		dispatch(ctx, msg, reply)
	}
}

func sendDenyNotice(ctx context.Context, entry RegistryEntry, msg IncomingMessage, reply Replier, limiter *denyNoticeLimiter) {
	if reply == nil || limiter == nil {
		return
	}
	key := string(entry.Platform.Name()) + "\x00" + entry.Platform.AccountID() + "\x00" + msg.UserID
	if !limiter.Allow(key) {
		return
	}
	if err := reply.SendText(ctx, denyNoticeText); err != nil {
		log.Printf("[platform] failed to send deny notice to %s user %q: %v", entry.Platform.Name(), msg.UserID, err)
	}
}

type denyNoticeLimiter struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	last   map[string]time.Time
}

func newDenyNoticeLimiter(window time.Duration) *denyNoticeLimiter {
	return &denyNoticeLimiter{
		window: window,
		now:    time.Now,
		last:   make(map[string]time.Time),
	}
}

func (l *denyNoticeLimiter) Allow(key string) bool {
	if l == nil || key == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if last, ok := l.last[key]; ok && now.Sub(last) < l.window {
		return false
	}
	l.last[key] = now
	return true
}
