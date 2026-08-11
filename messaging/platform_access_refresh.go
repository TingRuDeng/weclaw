package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

func (h *Handler) refreshFeishuAccountAccess(accountID string, allowedUsers []string) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return
	}
	h.mu.RLock()
	update := h.platformAccessUpdater
	h.mu.RUnlock()
	if update == nil {
		return
	}
	update(platform.PlatformFeishu, accountID, append([]string(nil), allowedUsers...))
}
