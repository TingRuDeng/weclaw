package messaging

import (
	"context"
	"sort"
	"strings"
	"sync"
)

type claudeSessionLockRequest struct {
	ctx        context.Context
	command    string
	sessionIDs []string
}

// lockClaudeSessionControls 在统一等待预算内按稳定顺序获取多个 session 控制锁。
func (h *Handler) lockClaudeSessionControls(req claudeSessionLockRequest) (func(), error) {
	sessionIDs := sortedUniqueClaudeSessionIDs(req.sessionIDs)
	waitCtx, cancel := context.WithTimeout(normalizeContext(req.ctx), h.codexSessionLockWaitTimeoutValue())
	defer cancel()
	unlocks := make([]func(), 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		unlock, err := h.lockClaudeSessionControlContext(waitCtx, sessionID)
		if err != nil {
			releaseClaudeSessionControlLocks(unlocks)
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	var once sync.Once
	return func() { once.Do(func() { releaseClaudeSessionControlLocks(unlocks) }) }, nil
}

func sortedUniqueClaudeSessionIDs(sessionIDs []string) []string {
	unique := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
			unique[sessionID] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for sessionID := range unique {
		result = append(result, sessionID)
	}
	sort.Strings(result)
	return result
}

// releaseClaudeSessionControlLocks 逆序释放已获取锁，保持严格栈语义。
func releaseClaudeSessionControlLocks(unlocks []func()) {
	for index := len(unlocks) - 1; index >= 0; index-- {
		unlocks[index]()
	}
}
