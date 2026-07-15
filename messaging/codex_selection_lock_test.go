package messaging

import (
	"runtime"
	"testing"
	"time"
)

const selectionLockObservationTimeout = 2 * time.Second

type executionLockUsersExpectation struct {
	h     *Handler
	key   string
	users int
}

// waitForExecutionLockUsers 观察真实 execution lock 引用数；deadline 只用于失败退出。
func waitForExecutionLockUsers(t *testing.T, want executionLockUsersExpectation) {
	t.Helper()
	deadline := time.Now().Add(selectionLockObservationTimeout)
	for {
		want.h.taskLocksMu.Lock()
		lock := want.h.taskLocks[want.key]
		users := 0
		if lock != nil {
			users = lock.users
		}
		want.h.taskLocksMu.Unlock()
		if users == want.users {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution lock %q users=%d，want %d", want.key, users, want.users)
		}
		runtime.Gosched()
	}
}

func countActiveTasks(h *Handler) int {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	return len(h.activeTasks)
}
