package messaging

import "github.com/fastclaw-ai/weclaw/agent"

// bindCodexThreadActivityEvents 把 Agent 的 thread 状态变化收敛为
// follower reconciler 的非阻塞唤醒。权威状态仍在调和中重新读取。
func (h *Handler) bindCodexThreadActivityEvents(ag agent.Agent) {
	source, ok := ag.(agent.CodexThreadActivityEventSource)
	if !ok {
		return
	}
	source.SetCodexThreadActivityHandler(func(string) {
		h.wakeCodexFollowerReconciler()
	})
}

func (h *Handler) wakeCodexFollowerReconciler() {
	h.codexFollowerMu.Lock()
	service := h.codexFollower
	h.codexFollowerMu.Unlock()
	if service == nil {
		return
	}
	select {
	case service.wake <- struct{}{}:
	default:
	}
}
