package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	authMaxFailures = 10              // 窗口内最大失败次数
	authWindow      = 1 * time.Minute // 滑动窗口
	authBlockFor    = 1 * time.Minute // 触发后封禁时长
)

// authThrottle 对 API token 校验失败做按来源限速，缓解暴力破解。
type authThrottle struct {
	mu          sync.Mutex
	fails       map[string][]time.Time
	blocked_    map[string]time.Time
	now         func() time.Time
	lastCleanup time.Time
}

func newAuthThrottle() *authThrottle {
	return &authThrottle{
		fails:    make(map[string][]time.Time),
		blocked_: make(map[string]time.Time),
		now:      time.Now,
	}
}

// blocked 报告该来源是否处于封禁期。
func (t *authThrottle) blocked(key string) bool {
	if t == nil || key == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupExpiredLocked(now)
	until, ok := t.blocked_[key]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(t.blocked_, key)
		delete(t.fails, key)
		return false
	}
	return true
}

// fail 记录一次失败；窗口内累计达到上限则封禁。
func (t *authThrottle) fail(key string) {
	if t == nil || key == "" {
		return
	}
	now := t.now()
	cutoff := now.Add(-authWindow)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupExpiredLocked(now)
	kept := t.fails[key][:0]
	for _, at := range t.fails[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	t.fails[key] = kept
	if len(kept) >= authMaxFailures {
		t.blocked_[key] = now.Add(authBlockFor)
	}
}

func (t *authThrottle) cleanupExpiredLocked(now time.Time) {
	if !t.lastCleanup.IsZero() && now.Sub(t.lastCleanup) < authWindow {
		return
	}
	cutoff := now.Add(-authWindow)
	for key, failures := range t.fails {
		kept := failures[:0]
		for _, failure := range failures {
			if failure.After(cutoff) {
				kept = append(kept, failure)
			}
		}
		if len(kept) == 0 {
			delete(t.fails, key)
		} else {
			t.fails[key] = kept
		}
	}
	for key, until := range t.blocked_ {
		if now.After(until) {
			delete(t.blocked_, key)
		}
	}
	t.lastCleanup = now
}

// reset 在成功鉴权后清除该来源的失败计数。
func (t *authThrottle) reset(key string) {
	if t == nil || key == "" {
		return
	}
	t.mu.Lock()
	delete(t.fails, key)
	delete(t.blocked_, key)
	t.mu.Unlock()
}

// clientKey 取请求来源 IP 作为限速键。
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
