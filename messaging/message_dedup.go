package messaging

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// cleanSeenMsgs 清理超过 TTL 的消息去重缓存。
func (h *Handler) cleanSeenMsgs(ttl time.Duration) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	cutoff := time.Now().Add(-ttl)
	h.seenMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenMsgs.Delete(key)
		}
		return true
	})
	h.seenTextMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenTextMsgs.Delete(key)
		}
		return true
	})
}

// maybeCleanSeenMsgs 每个 TTL 窗口最多清理一次，避免为每条消息创建后台 goroutine。
func (h *Handler) maybeCleanSeenMsgs(now time.Time) {
	ttl := h.duplicateTTL()
	last := h.lastDedupCleanup.Load()
	if last > 0 && now.Sub(time.Unix(0, last)) < ttl {
		return
	}
	if !h.lastDedupCleanup.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	h.cleanSeenMsgs(ttl)
}

func (h *Handler) duplicateTTL() time.Duration {
	cfg := h.resolveProgressConfig("")
	return durationSeconds(cfg.DuplicateTTLSeconds, 5*time.Minute)
}

func (h *Handler) isDuplicateTextMessage(userID string, contextToken string, routeUserID string, text string) bool {
	key := buildTextDedupKey(userID, contextToken, text)
	if key == "" {
		return false
	}
	now := time.Now()
	if seenAt, loaded := h.seenTextMsgs.LoadOrStore(key, now); loaded {
		if t, ok := seenAt.(time.Time); ok && now.Sub(t) <= h.duplicateTTL() {
			if h.hasMatchingActiveTextTask(userID, routeUserID, text) {
				return true
			}
			h.seenTextMsgs.Store(key, now)
			return false
		}
		h.seenTextMsgs.Store(key, now)
	}
	h.maybeCleanSeenMsgs(now)
	return false
}

// hasMatchingActiveTextTask 只在同一用户的对应任务仍运行时拦截无消息 ID 的重复投递。
func (h *Handler) hasMatchingActiveTextTask(userID string, routeUserID string, text string) bool {
	owner := strings.TrimSpace(userID)
	route := strings.TrimSpace(routeUserID)
	fingerprint := normalizedTextFingerprint(text)
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	for _, task := range h.tasks.active {
		task.mu.Lock()
		matched := task.owner == owner && task.routeUserID == route && task.messageFingerprint == fingerprint && task.phase != codexTaskTerminal
		task.mu.Unlock()
		if matched {
			return true
		}
	}
	return false
}

func normalizedTextFingerprint(text string) string {
	normalized := strings.Join(strings.Fields(text), " ")
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func buildTextDedupKey(userID string, contextToken string, text string) string {
	normalized := strings.Join(strings.Fields(text), " ")
	if userID == "" || normalized == "" {
		return ""
	}
	return userID + "\x00" + contextToken + "\x00" + normalized
}

func duplicateTaskReply() string {
	return "这条任务已经收到，正在处理中。\n\n完成后我会发送完整结果。"
}
