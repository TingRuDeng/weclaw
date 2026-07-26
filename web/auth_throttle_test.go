package web

import (
	"testing"
	"time"
)

func TestAuthThrottleBlocksAfterMaxFailures(t *testing.T) {
	now := time.Unix(0, 0)
	th := newAuthThrottleWithClock(func() time.Time { return now })

	for i := 0; i < authMaxFailures-1; i++ {
		th.Fail("1.2.3.4")
		if th.Blocked("1.2.3.4") {
			t.Fatalf("should not block before reaching max (i=%d)", i)
		}
	}
	th.Fail("1.2.3.4") // 第 authMaxFailures 次
	if !th.Blocked("1.2.3.4") {
		t.Fatal("should block after reaching max failures")
	}
	// 其它来源不受影响
	if th.Blocked("5.6.7.8") {
		t.Fatal("throttle must be per-source")
	}
	// 封禁到期后解除
	now = now.Add(authBlockFor + time.Second)
	if th.Blocked("1.2.3.4") {
		t.Fatal("block should expire after authBlockFor")
	}
}

func TestAuthThrottleResetOnSuccess(t *testing.T) {
	now := time.Unix(0, 0)
	th := newAuthThrottleWithClock(func() time.Time { return now })
	for i := 0; i < authMaxFailures-1; i++ {
		th.Fail("1.2.3.4")
	}
	th.Reset("1.2.3.4")
	th.Fail("1.2.3.4")
	if th.Blocked("1.2.3.4") {
		t.Fatal("reset should clear prior failure count")
	}
}

func TestAuthThrottleRemovesExpiredSources(t *testing.T) {
	now := time.Unix(100, 0)
	th := newAuthThrottleWithClock(func() time.Time { return now })
	th.Fail("source")
	now = now.Add(authWindow + authBlockFor + time.Second)
	for i := 0; i < authMaxFailures-1; i++ {
		th.Fail("source")
	}
	if th.Blocked("source") {
		t.Fatal("failure outside the active window was retained")
	}
	th.Fail("source")
	if !th.Blocked("source") {
		t.Fatal("current-window failures did not reach the block threshold")
	}
}
