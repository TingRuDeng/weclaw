package messaging

import (
	"context"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// unsubscribeCodexThreadIfUnusedLocked releases only this app-server
// connection's subscription. The caller must hold the thread control lock so a
// concurrent bind or turn admission cannot appear between the final checks and
// thread/unsubscribe.
func (h *Handler) unsubscribeCodexThreadIfUnusedLocked(
	ctx context.Context,
	subscriptionAgent agent.CodexThreadSubscriptionAgent,
	threadID string,
) (bool, error) {
	threadID = strings.TrimSpace(threadID)
	if subscriptionAgent == nil || threadID == "" {
		return false, nil
	}
	if h.ensureCodexSessions().activeFrontendUsesThread(threadID) ||
		h.hasNonterminalCodexTaskForThread(threadID) {
		return false, nil
	}
	return subscriptionAgent.UnsubscribeCodexThread(ctx, threadID)
}

func (h *Handler) unsubscribeCodexThreadAfterTask(agentName string, threadID string) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	subscriptionAgent, ok := h.AgentByName(strings.TrimSpace(agentName)).(agent.CodexThreadSubscriptionAgent)
	if !ok {
		return
	}
	ctx, cancel := h.codexThreadControlContext(context.Background())
	defer cancel()
	unlock, err := h.lockCodexThreadControlContext(ctx, threadID)
	if err != nil {
		log.Printf("[codex-subscription] 等待终态退订锁失败 thread=%q: %v", threadID, err)
		return
	}
	defer unlock()
	if _, err := h.unsubscribeCodexThreadIfUnusedLocked(ctx, subscriptionAgent, threadID); err != nil {
		log.Printf("[codex-subscription] 终态退订失败 thread=%q: %v", threadID, err)
	}
}
