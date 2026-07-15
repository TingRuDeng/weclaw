package messaging

import (
	"context"
	"strings"
	"sync"
)

const (
	codexThreadControlExecutionPrefix   = "codex-thread-control\x00"
	claudeSessionControlExecutionPrefix = "claude-session-control\x00"
)

type executionLock struct {
	token chan struct{}
	users int
}

// lockAgentExecution 串行同一执行通道，并在最后一个使用者离开后回收锁。
func (h *Handler) lockAgentExecution(key string) func() {
	unlock, err := h.lockAgentExecutionContext(context.Background(), key)
	if err != nil {
		panic("background execution lock was canceled")
	}
	return unlock
}

// lockAgentExecutionContext 在等待同一执行通道时响应 context 取消。
func (h *Handler) lockAgentExecutionContext(ctx context.Context, key string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lock := h.retainExecutionLock(key)
	select {
	case <-lock.token:
		var once sync.Once
		return func() {
			once.Do(func() {
				lock.token <- struct{}{}
				h.releaseExecutionLock(key, lock)
			})
		}, nil
	case <-ctx.Done():
		h.releaseExecutionLock(key, lock)
		return nil, ctx.Err()
	}
}

func (h *Handler) retainExecutionLock(key string) *executionLock {
	h.taskLocksMu.Lock()
	defer h.taskLocksMu.Unlock()
	if h.taskLocks == nil {
		h.taskLocks = make(map[string]*executionLock)
	}
	lock := h.taskLocks[key]
	if lock == nil {
		lock = &executionLock{token: make(chan struct{}, 1)}
		lock.token <- struct{}{}
		h.taskLocks[key] = lock
	}
	lock.users++
	return lock
}

func (h *Handler) releaseExecutionLock(key string, lock *executionLock) {
	h.taskLocksMu.Lock()
	defer h.taskLocksMu.Unlock()
	lock.users--
	if lock.users == 0 && h.taskLocks[key] == lock {
		delete(h.taskLocks, key)
	}
}

// lockCodexThreadControl 串行同一 thread 的控制权移交、运行时探测与任务准入。
func (h *Handler) lockCodexThreadControl(threadID string) func() {
	unlock, err := h.lockCodexThreadControlContext(context.Background(), threadID)
	if err != nil {
		panic("background Codex thread control lock was canceled")
	}
	return unlock
}

// lockCodexThreadControlContext 串行 thread 控制操作，并允许等待方取消。
func (h *Handler) lockCodexThreadControlContext(ctx context.Context, threadID string) (func(), error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return func() {}, nil
	}
	return h.lockAgentExecutionContext(ctx, codexThreadControlExecutionPrefix+threadID)
}

// lockClaudeSessionControl 串行同一 Claude session 的控制权变化与任务准入。
func (h *Handler) lockClaudeSessionControl(sessionID string) func() {
	unlock, err := h.lockClaudeSessionControlContext(context.Background(), sessionID)
	if err != nil {
		panic("background Claude session control lock was canceled")
	}
	return unlock
}

// lockClaudeSessionControlContext 串行 session 控制操作，并允许等待方取消。
func (h *Handler) lockClaudeSessionControlContext(ctx context.Context, sessionID string) (func(), error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return func() {}, nil
	}
	return h.lockAgentExecutionContext(ctx, claudeSessionControlExecutionPrefix+sessionID)
}
