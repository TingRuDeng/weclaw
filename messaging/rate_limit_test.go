package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestUserRateLimiterWindow(t *testing.T) {
	now := time.Unix(0, 0)
	l := newUserRateLimiter(time.Minute)
	l.now = func() time.Time { return now }

	// limit=2：前两次放行，第三次拒绝
	if !l.Allow("u1", 2) {
		t.Fatal("first call should be allowed")
	}
	if !l.Allow("u1", 2) {
		t.Fatal("second call should be allowed")
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

func TestUserRateLimiterRemovesExpiredKeys(t *testing.T) {
	now := time.Unix(100, 0)
	l := newUserRateLimiter(time.Minute)
	l.now = func() time.Time { return now }
	if !l.Allow("expired", 1) {
		t.Fatal("initial call should be allowed")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("current", 1) {
		t.Fatal("current call should be allowed")
	}
	if _, ok := l.hits["expired"]; ok {
		t.Fatal("expired limiter key was not removed")
	}
}

func TestHandlerRateLimitGate(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetRateLimitPerMinute(2)
	if !h.allowAgentInvocation(platform.PlatformWeChat, "account-1", "u1") {
		t.Fatal("first invocation should pass")
	}
	if !h.allowAgentInvocation(platform.PlatformWeChat, "account-1", "u1") {
		t.Fatal("second invocation should pass")
	}
	if h.allowAgentInvocation(platform.PlatformWeChat, "account-1", "u1") {
		t.Fatal("third invocation should be throttled")
	}
	if !h.allowAgentInvocation(platform.PlatformWeChat, "account-1", "u2") {
		t.Fatal("other user must be independent")
	}
}

func TestHandlerRateLimitUsesRealActorInsteadOfConversationRoute(t *testing.T) {
	newRuntime := func(userID, routeUserID string, reply platform.Replier) platformMessageRuntime {
		return platformMessageRuntime{
			ctx: context.Background(),
			msg: platform.IncomingMessage{
				Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: userID,
			},
			reply: reply, routeUserID: routeUserID, text: "普通问题",
		}
	}
	t.Run("同群不同用户额度独立", func(t *testing.T) {
		h := NewHandler(nil, nil)
		h.SetRateLimitPerMinute(1)
		first := platformtest.NewReplier(platform.Capabilities{Text: true})
		second := platformtest.NewReplier(platform.Capabilities{Text: true})
		h.dispatchPlatformMessage(newRuntime("ou_a", "feishu:chat:group-1", first))
		h.dispatchPlatformMessage(newRuntime("ou_b", "feishu:chat:group-1", second))
		if replyContains(second.Texts, "请求过于频繁") {
			t.Fatalf("同群不同用户不应共享限流额度，replies=%#v", second.Texts)
		}
	})
	t.Run("同一用户跨会话共享额度", func(t *testing.T) {
		h := NewHandler(nil, nil)
		h.SetRateLimitPerMinute(1)
		first := platformtest.NewReplier(platform.Capabilities{Text: true})
		second := platformtest.NewReplier(platform.Capabilities{Text: true})
		h.dispatchPlatformMessage(newRuntime("ou_a", "feishu:chat:group-1", first))
		h.dispatchPlatformMessage(newRuntime("ou_a", "feishu:chat:group-2", second))
		if !replyContains(second.Texts, "请求过于频繁") {
			t.Fatalf("同一平台账号用户跨会话应共享限流额度，replies=%#v", second.Texts)
		}
	})
}

func replyContains(replies []string, text string) bool {
	for _, reply := range replies {
		if strings.Contains(reply, text) {
			return true
		}
	}
	return false
}
