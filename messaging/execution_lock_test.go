package messaging

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExecutionLockContextCancellationDoesNotLeak(t *testing.T) {
	h := NewHandler(nil, nil)
	unlockHolder := h.lockAgentExecution("shared-key")

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	waiterUnlock, err := h.lockAgentExecutionContext(waitCtx, "shared-key")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待锁错误 = %v，期望 deadline exceeded", err)
	}
	if waiterUnlock != nil {
		t.Fatal("取消的等待者不应获得 unlock 函数")
	}

	unlockHolder()
	assertExecutionLockReusable(t, h, "shared-key")
	assertExecutionLockReleased(t, h, "shared-key")
}

func TestExecutionLockUnlockIsIdempotent(t *testing.T) {
	h := NewHandler(nil, nil)
	unlock := h.lockAgentExecution("shared-key")

	unlock()
	unlock()

	assertExecutionLockReusable(t, h, "shared-key")
	assertExecutionLockReleased(t, h, "shared-key")
}

func assertExecutionLockReusable(t *testing.T, h *Handler, key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	unlock, err := h.lockAgentExecutionContext(ctx, key)
	if err != nil {
		t.Fatalf("重新获取锁失败: %v", err)
	}
	unlock()
}

func assertExecutionLockReleased(t *testing.T, h *Handler, key string) {
	t.Helper()
	h.taskLocksMu.Lock()
	defer h.taskLocksMu.Unlock()
	if _, exists := h.taskLocks[key]; exists {
		t.Fatalf("锁 %q 未从索引中回收", key)
	}
}
