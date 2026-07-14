package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

const testAdminCommandTimeout = 20 * time.Millisecond

// TestAdminCommandTimeoutStartsAfterQueueLock 验证排队时间不会消耗命令执行超时。
func TestAdminCommandTimeoutStartsAfterQueueLock(t *testing.T) {
	h := NewHandler(nil, nil)
	h.adminTimeout = testAdminCommandTimeout
	executed := make(chan error, 1)
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, _ string, _ []string) (string, error) {
		executed <- ctx.Err()
		return "Already up to date", nil
	})

	h.serviceAdminMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runServiceAdminCommand(platform.IncomingMessage{UserID: "admin"}, "update", nil, newAdminCommandTestReplier())
	}()
	time.Sleep(2 * testAdminCommandTimeout)
	h.serviceAdminMu.Unlock()

	select {
	case err := <-executed:
		if err != nil {
			t.Fatalf("执行器拿到的上下文已提前失效: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("管理员命令未执行")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("管理员命令未结束")
	}
}
