package auththrottle

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	DefaultMaxFailures = 10
	DefaultMaxSources  = 4096
	DefaultWindow      = time.Minute
	DefaultBlockFor    = time.Minute
)

// Throttle limits repeated authentication failures per actual TCP source.
type Throttle struct {
	mu          sync.Mutex
	failures    map[string][]time.Time
	blocked     map[string]time.Time
	lastSeen    map[string]time.Time
	now         func() time.Time
	lastCleanup time.Time
}

func New() *Throttle {
	return NewWithClock(time.Now)
}

// NewWithClock exposes a deterministic clock for package consumers' tests.
func NewWithClock(now func() time.Time) *Throttle {
	if now == nil {
		now = time.Now
	}
	return &Throttle{
		failures: make(map[string][]time.Time),
		blocked:  make(map[string]time.Time),
		lastSeen: make(map[string]time.Time),
		now:      now,
	}
}

// Blocked reports whether key is currently blocked.
func (t *Throttle) Blocked(key string) bool {
	if t == nil || key == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupExpiredLocked(now)
	until, ok := t.blocked[key]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(t.blocked, key)
		delete(t.failures, key)
		return false
	}
	return true
}

// Fail records one authentication failure and blocks at the shared threshold.
func (t *Throttle) Fail(key string) {
	if t == nil || key == "" {
		return
	}
	now := t.now()
	cutoff := now.Add(-DefaultWindow)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupExpiredLocked(now)
	kept := t.failures[key][:0]
	for _, at := range t.failures[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	t.failures[key] = kept
	t.lastSeen[key] = now
	if len(kept) >= DefaultMaxFailures {
		t.blocked[key] = now.Add(DefaultBlockFor)
	}
	t.enforceSourceLimitLocked()
}

// Reset clears failures after a successful authentication.
func (t *Throttle) Reset(key string) {
	if t == nil || key == "" {
		return
	}
	t.mu.Lock()
	delete(t.failures, key)
	delete(t.blocked, key)
	delete(t.lastSeen, key)
	t.mu.Unlock()
}

func (t *Throttle) cleanupExpiredLocked(now time.Time) {
	if !t.lastCleanup.IsZero() && now.Sub(t.lastCleanup) < DefaultWindow {
		return
	}
	cutoff := now.Add(-DefaultWindow)
	for key, failures := range t.failures {
		kept := failures[:0]
		for _, failure := range failures {
			if failure.After(cutoff) {
				kept = append(kept, failure)
			}
		}
		if len(kept) == 0 {
			delete(t.failures, key)
			delete(t.lastSeen, key)
		} else {
			t.failures[key] = kept
		}
	}
	for key, until := range t.blocked {
		if !now.Before(until) {
			delete(t.blocked, key)
		}
	}
	t.lastCleanup = now
}

func (t *Throttle) enforceSourceLimitLocked() {
	for len(t.lastSeen) > DefaultMaxSources {
		// Eviction is intentionally O(1) best-effort instead of maintaining an
		// attacker-controlled LRU queue. The token remains the authority; this
		// bound prevents source churn from becoming an unbounded memory sink.
		evictedKey := ""
		for key := range t.lastSeen {
			evictedKey = key
			break
		}
		delete(t.failures, evictedKey)
		delete(t.blocked, evictedKey)
		delete(t.lastSeen, evictedKey)
	}
}

// ClientKey derives the rate-limit key from RemoteAddr and ignores forwarded headers.
func ClientKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
