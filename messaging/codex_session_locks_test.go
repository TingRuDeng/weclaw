package messaging

import (
	"context"
	"testing"
	"time"
)

// TestLockCodexSessionThreadsReleasesPartialLocksOnTimeout 验证部分加锁失败时已持有锁会被释放。
func TestLockCodexSessionThreadsReleasesPartialLocksOnTimeout(t *testing.T) {
	h := NewHandler(nil, nil)
	h.codexLockWaitTimeout = 20 * time.Millisecond
	unlockB := h.lockCodexThreadControl("thread-b")
	_, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
		ctx: context.Background(), command: "switch", threadIDs: []string{"thread-b", "thread-a"},
	})
	if !isCodexSessionControlTimeout(err) {
		t.Fatalf("error=%v", err)
	}
	unlockB()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlockA, err := h.lockCodexThreadControlContext(ctx, "thread-a")
	if err != nil {
		t.Fatalf("部分失败后 thread-a 锁未释放: %v", err)
	}
	unlockA()
}

// TestLockCodexSessionThreadsUsesStableOrder 验证不同输入顺序始终按稳定顺序获取多把锁。
func TestLockCodexSessionThreadsUsesStableOrder(t *testing.T) {
	h := NewHandler(nil, nil)
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, ids := range [][]string{{"thread-a", "thread-b"}, {"thread-b", "thread-a"}} {
		ids := ids
		go func() {
			<-start
			unlock, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
				ctx: context.Background(), command: "switch", threadIDs: ids,
			})
			if err == nil {
				unlock()
			}
			done <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
