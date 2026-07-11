package platform

import (
	"context"
	"log"
	"sync"
	"time"
)

func sendDenyNotice(ctx context.Context, entry RegistryEntry, msg IncomingMessage, reply Replier, limiter *denyNoticeLimiter, denyProvider DenyNoticeProvider) {
	if reply == nil || limiter == nil {
		return
	}
	key := string(entry.Platform.Name()) + "\x00" + entry.Platform.AccountID() + "\x00" + msg.UserID
	if !limiter.Allow(key) {
		return
	}
	text := denyNoticeText
	if denyProvider != nil {
		if provided := denyProvider(msg); provided != "" {
			text = provided
		}
	}
	if err := reply.SendText(ctx, text); err != nil {
		log.Printf("[platform] failed to send deny notice to %s user %q: %v", entry.Platform.Name(), msg.UserID, err)
	}
}

type denyNoticeLimiter struct {
	mu          sync.Mutex
	window      time.Duration
	now         func() time.Time
	last        map[string]time.Time
	lastCleanup time.Time
}

func newDenyNoticeLimiter(window time.Duration) *denyNoticeLimiter {
	return &denyNoticeLimiter{window: window, now: time.Now, last: make(map[string]time.Time)}
}

func (l *denyNoticeLimiter) Allow(key string) bool {
	if l == nil || key == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.cleanupExpiredLocked(now)
	if last, ok := l.last[key]; ok && now.Sub(last) < l.window {
		return false
	}
	l.last[key] = now
	return true
}

func (l *denyNoticeLimiter) cleanupExpiredLocked(now time.Time) {
	if !l.lastCleanup.IsZero() && now.Sub(l.lastCleanup) < l.window {
		return
	}
	for key, last := range l.last {
		if now.Sub(last) >= l.window {
			delete(l.last, key)
		}
	}
	l.lastCleanup = now
}
