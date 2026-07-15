package messaging

import (
	"context"
	"reflect"
	"testing"
	"time"
)

const (
	codexLockTestWaitTimeout        = 2 * time.Second
	codexLockTestObservationTimeout = 20 * time.Millisecond
	expectedCodexLockUsers          = 2
)

// TestSortedUniqueCodexThreadIDs 验证多锁依赖的 thread ID 清洗、去重与升序契约。
func TestSortedUniqueCodexThreadIDs(t *testing.T) {
	got := sortedUniqueCodexThreadIDs([]string{" thread-b ", "", "thread-a", "thread-b", "\t"})
	want := []string{"thread-a", "thread-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("thread IDs=%v, want=%v", got, want)
	}
}

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

// TestLockCodexSessionThreadsUsesStableOrder 验证批量锁不按输入顺序形成 ABBA 风险。
func TestLockCodexSessionThreadsUsesStableOrder(t *testing.T) {
	h := NewHandler(nil, nil)
	h.codexLockWaitTimeout = codexLockTestWaitTimeout
	unlockB := h.lockCodexThreadControl("thread-b")
	defer unlockB()
	done := make(chan error, 1)
	go func() {
		unlock, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
			ctx: context.Background(), command: "switch", threadIDs: []string{"thread-b", "thread-a"},
		})
		if err == nil {
			unlock()
		}
		done <- err
	}()
	waitForCodexThreadLockWaiter(t, h, "thread-b")
	ctx, cancel := context.WithTimeout(context.Background(), codexLockTestObservationTimeout)
	defer cancel()
	unlockA, err := h.lockCodexThreadControlContext(ctx, "thread-a")
	if err == nil {
		unlockA()
		t.Fatal("批量锁未先获取排序后的 thread-a")
	}
	if !isCodexSessionControlTimeout(err) {
		t.Fatalf("探测 thread-a 锁失败: %v", err)
	}
	unlockB()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// waitForCodexThreadLockWaiter 等待批量请求进入指定 thread 的真实锁等待队列。
func waitForCodexThreadLockWaiter(t *testing.T, h *Handler, threadID string) {
	t.Helper()
	deadline := time.Now().Add(codexLockTestWaitTimeout)
	key := codexThreadControlExecutionPrefix + threadID
	for time.Now().Before(deadline) {
		h.taskLocksMu.Lock()
		lock := h.taskLocks[key]
		users := 0
		if lock != nil {
			users = lock.users
		}
		h.taskLocksMu.Unlock()
		if users == expectedCodexLockUsers {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("未观察到 %s 的批量锁等待者", threadID)
}
