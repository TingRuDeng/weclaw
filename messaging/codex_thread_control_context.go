package messaging

import (
	"context"
	"time"
)

const defaultCodexThreadControlTimeout = 15 * time.Second

// codexThreadControlTimeoutValue 限制 thread 控制锁等待及锁内 RPC 的总时长。
// 调用方更短的 deadline 仍由 context.WithTimeout 保留。
func (h *Handler) codexThreadControlTimeoutValue() time.Duration {
	if h.codexControlTimeout > 0 {
		return h.codexControlTimeout
	}
	return defaultCodexThreadControlTimeout
}

func (h *Handler) codexThreadControlContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(normalizeContext(ctx), h.codexThreadControlTimeoutValue())
}
