package messaging

import (
	"testing"
	"time"
)

func TestUserRateLimiterWindow(t *testing.T) {
	now := time.Unix(0, 0)
	l := newUserRateLimiter(time.Minute)
	l.now = func() time.Time { return now }

	// limit=2：前两次放行，第三次拒绝
	if !l.Allow("u1", 2) || !l.Allow("u1", 2) {
		t.Fatal("first two calls should be allowed")
	}
	if l.Allow("u1", 2) {
		t.Fatal("third call within window should be denied")
	}
	// 其他用户独立
	if !l.Allow("u2", 2) {
		t.Fatal("different user should be allowed")
	}
	// 窗口滑出后恢复
	now = now.Add(61 * time.Second)
	if !l.Allow("u1", 2) {
		t.Fatal("call after window should be allowed again")
	}
}

func TestUserRateLimiterDisabled(t *testing.T) {
	l := newUserRateLimiter(time.Minute)
	for i := 0; i < 100; i++ {
		if !l.Allow("u1", 0) {
			t.Fatal("limit<=0 must never deny")
		}
	}
}

func TestHandlerRateLimitGate(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetRateLimitPerMinute(2)
	if !h.allowAgentInvocation("wechat:u1") || !h.allowAgentInvocation("wechat:u1") {
		t.Fatal("first two invocations should pass")
	}
	if h.allowAgentInvocation("wechat:u1") {
		t.Fatal("third invocation should be throttled")
	}
	if !h.allowAgentInvocation("wechat:u2") {
		t.Fatal("other user must be independent")
	}
}
