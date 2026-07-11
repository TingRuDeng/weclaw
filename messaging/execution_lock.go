package messaging

import "sync"

type executionLock struct {
	mu    sync.Mutex
	users int
}

// lockAgentExecution 串行同一执行通道，并在最后一个使用者离开后回收锁。
func (h *Handler) lockAgentExecution(key string) func() {
	h.taskLocksMu.Lock()
	if h.taskLocks == nil {
		h.taskLocks = make(map[string]*executionLock)
	}
	lock := h.taskLocks[key]
	if lock == nil {
		lock = &executionLock{}
		h.taskLocks[key] = lock
	}
	lock.users++
	h.taskLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		h.taskLocksMu.Lock()
		lock.users--
		if lock.users == 0 && h.taskLocks[key] == lock {
			delete(h.taskLocks, key)
		}
		h.taskLocksMu.Unlock()
	}
}
