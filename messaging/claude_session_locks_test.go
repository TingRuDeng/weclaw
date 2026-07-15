package messaging

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestClaudeSessionLocksSortAndDeduplicateIDs(t *testing.T) {
	got := sortedUniqueClaudeSessionIDs([]string{" session-b ", "", "session-a", "session-b", "\t"})
	want := []string{"session-a", "session-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session IDs=%v, want=%v", got, want)
	}
}

func TestClaudeSessionLocksAvoidABBAAndRecycleLocks(t *testing.T) {
	h := NewHandler(nil, nil)
	h.codexLockWaitTimeout = 2 * time.Second
	unblock := h.lockClaudeSessionControl("session-a")
	results := make(chan error, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for _, ids := range [][]string{{"session-a", "session-b"}, {"session-b", "session-a"}} {
		ids := ids
		go func() {
			ready.Done()
			<-start
			unlock, err := h.lockClaudeSessionControls(claudeSessionLockRequest{ctx: context.Background(), command: "switch", sessionIDs: ids})
			if err == nil {
				unlock()
			}
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	waitForClaudeSessionLockWaiters(t, h, "session-a", 3)
	unblock()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	h.taskLocksMu.Lock()
	defer h.taskLocksMu.Unlock()
	if len(h.taskLocks) != 0 {
		t.Fatalf("execution locks 未回收: %+v", h.taskLocks)
	}
}

func TestClaudeSessionLocksUseSharedContextBudgetAndReleasePartialLocks(t *testing.T) {
	h := NewHandler(nil, nil)
	h.codexLockWaitTimeout = 30 * time.Millisecond
	unblockB := h.lockClaudeSessionControl("session-b")
	started := time.Now()
	_, err := h.lockClaudeSessionControls(claudeSessionLockRequest{
		ctx: context.Background(), command: "switch", sessionIDs: []string{"session-b", "session-a"},
	})
	elapsed := time.Since(started)
	if !isCodexSessionControlTimeout(err) {
		t.Fatalf("error=%v", err)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("没有共享等待预算: %s", elapsed)
	}
	unblockB()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlockA, err := h.lockClaudeSessionControlContext(ctx, "session-a")
	if err != nil {
		t.Fatalf("部分失败后 session-a 锁未释放: %v", err)
	}
	unlockA()
}

func TestClaudeSessionLocksReleaseInReverseOrder(t *testing.T) {
	order := make([]int, 0, 3)
	releaseClaudeSessionControlLocks([]func(){
		func() { order = append(order, 1) },
		func() { order = append(order, 2) },
		func() { order = append(order, 3) },
	})
	if !reflect.DeepEqual(order, []int{3, 2, 1}) {
		t.Fatalf("release order=%v", order)
	}
}

func waitForClaudeSessionLockWaiters(t *testing.T, h *Handler, sessionID string, wantUsers int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	key := claudeSessionControlExecutionPrefix + sessionID
	for time.Now().Before(deadline) {
		h.taskLocksMu.Lock()
		lock := h.taskLocks[key]
		users := 0
		if lock != nil {
			users = lock.users
		}
		h.taskLocksMu.Unlock()
		if users == wantUsers {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("未观察到 %s 的 %d 个锁用户", sessionID, wantUsers)
}
