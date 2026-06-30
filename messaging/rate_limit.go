package messaging

import (
	"sync"
	"time"
)

// userRateLimiter 是按 key(通常是 routeUserID) 的滑动窗口限流器，
// 用于约束单用户触发 agent 的频率，防止刷屏 / 烧 token / 滥用。
type userRateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	hits   map[string][]time.Time
	now    func() time.Time
}

func newUserRateLimiter(window time.Duration) *userRateLimiter {
	if window <= 0 {
		window = time.Minute
	}
	return &userRateLimiter{
		window: window,
		hits:   make(map[string][]time.Time),
		now:    time.Now,
	}
}

// Allow 在窗口内命中数小于 limit 时记录本次并放行；limit<=0 表示不限流。
func (l *userRateLimiter) Allow(key string, limit int) bool {
	if l == nil || limit <= 0 || key == "" {
		return true
	}
	now := l.now()
	cutoff := now.Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}
