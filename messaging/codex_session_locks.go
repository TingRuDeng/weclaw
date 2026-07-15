package messaging

import (
	"context"
	"strings"
	"sync"
	"time"
)

type codexSessionThreadLockRequest struct {
	ctx       context.Context
	command   string
	threadIDs []string
}

// lockCodexSessionThreads 在统一等待预算内按稳定顺序获取多个 thread 控制锁。
func (h *Handler) lockCodexSessionThreads(req codexSessionThreadLockRequest) (func(), error) {
	threadIDs := sortedUniqueCodexThreadIDs(req.threadIDs)
	started := time.Now()
	waitCtx, cancel := context.WithTimeout(normalizeContext(req.ctx), h.codexSessionLockWaitTimeoutValue())
	defer cancel()
	unlocks := make([]func(), 0, len(threadIDs))
	for _, threadID := range threadIDs {
		unlock, err := h.lockCodexThreadControlContext(waitCtx, threadID)
		if err != nil {
			releaseCodexSessionThreadLocks(unlocks)
			logCodexSessionControlTimeout(
				req.command, "threads", strings.Join(threadIDs, ","), started, err,
			)
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	var once sync.Once
	return func() { once.Do(func() { releaseCodexSessionThreadLocks(unlocks) }) }, nil
}

// releaseCodexSessionThreadLocks 逆序释放已获取锁，与获取顺序形成严格栈语义。
func releaseCodexSessionThreadLocks(unlocks []func()) {
	for index := len(unlocks) - 1; index >= 0; index-- {
		unlocks[index]()
	}
}
