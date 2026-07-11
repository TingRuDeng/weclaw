package web

import (
	"testing"
	"time"
)

func TestAuthThrottleBlocksAfterMaxFailures(t *testing.T) {
	now := time.Unix(0, 0)
	th := newAuthThrottle()
	th.now = func() time.Time { return now }

	for i := 0; i < authMaxFailures-1; i++ {
		th.fail("1.2.3.4")
		if th.blocked("1.2.3.4") {
			t.Fatalf("should not block before reaching max (i=%d)", i)
		}
	}
	th.fail("1.2.3.4") // 第 authMaxFailures 次
	if !th.blocked("1.2.3.4") {
		t.Fatal("should block after reaching max failures")
	}
	// 其它来源不受影响
	if th.blocked("5.6.7.8") {
		t.Fatal("throttle must be per-source")
	}
	// 封禁到期后解除
	now = now.Add(authBlockFor + time.Second)
	if th.blocked("1.2.3.4") {
		t.Fatal("block should expire after authBlockFor")
	}
}

func TestAuthThrottleResetOnSuccess(t *testing.T) {
	now := time.Unix(0, 0)
	th := newAuthThrottle()
	th.now = func() time.Time { return now }
	for i := 0; i < authMaxFailures-1; i++ {
		th.fail("1.2.3.4")
	}
	th.reset("1.2.3.4")
	th.fail("1.2.3.4")
	if th.blocked("1.2.3.4") {
		t.Fatal("reset should clear prior failure count")
	}
}

func TestAuthThrottleRemovesExpiredSources(t *testing.T) {
	now := time.Unix(100, 0)
	th := newAuthThrottle()
	th.now = func() time.Time { return now }
	th.fail("expired")
	now = now.Add(authWindow + authBlockFor + time.Second)
	th.fail("current")

	if _, ok := th.fails["expired"]; ok {
		t.Fatal("expired failure source was not removed")
	}
	if _, ok := th.blocked_["expired"]; ok {
		t.Fatal("expired blocked source was not removed")
	}
}
