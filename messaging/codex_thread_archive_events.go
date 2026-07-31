package messaging

import (
	"log"

	"github.com/fastclaw-ai/weclaw/agent"
)

// bindCodexThreadArchiveEvents 让 Codex App 或其他 frontend 发起的归档同步清理消息路由。
func (h *Handler) bindCodexThreadArchiveEvents(ag agent.Agent) {
	source, ok := ag.(agent.CodexThreadArchiveEventSource)
	if !ok {
		return
	}
	source.SetCodexThreadArchivedHandler(func(threadID string) {
		if err := h.ensureCodexSessions().markRemoteThreadArchived(threadID); err != nil {
			log.Printf("[codex-session] failed to persist external thread archive (thread=%s): %v", threadID, err)
		}
	})
}
