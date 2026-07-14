package messaging

import (
	"strings"
	"sync"
)

const codexThreadControlExecutionPrefix = "codex-thread-control\x00"

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

// lockCodexThreadControl 串行同一 thread 的控制权移交、运行时探测与任务准入。
func (h *Handler) lockCodexThreadControl(threadID string) func() {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return func() {}
	}
	return h.lockAgentExecution(codexThreadControlExecutionPrefix + threadID)
}
