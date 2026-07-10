package messaging

import "strings"

type pendingCodexConfirmation struct {
	message string
	owner   string
}

// storePendingCodexConfirmation 保存等待用户回复“确认”的 Codex 消息及其发起人。
func (h *Handler) storePendingCodexConfirmation(key string, message string, owner string) {
	h.pendingCodexConfirmsMu.Lock()
	if h.pendingCodexConfirms == nil {
		h.pendingCodexConfirms = make(map[string]pendingCodexConfirmation)
	}
	h.pendingCodexConfirms[key] = pendingCodexConfirmation{message: message, owner: strings.TrimSpace(owner)}
	h.pendingCodexConfirmsMu.Unlock()
}

// takePendingCodexConfirmation 仅允许暂存人取出消息，保证群会话内不会越权执行。
func (h *Handler) takePendingCodexConfirmation(key string, actor string) (string, bool, bool) {
	h.pendingCodexConfirmsMu.Lock()
	defer h.pendingCodexConfirmsMu.Unlock()
	pending, ok := h.pendingCodexConfirms[key]
	if !ok || pending.message == "" {
		return "", false, false
	}
	if pending.owner != strings.TrimSpace(actor) {
		return "", false, true
	}
	delete(h.pendingCodexConfirms, key)
	return pending.message, true, false
}

// clearPendingCodexConfirmation 仅允许暂存人撤回待确认消息。
func (h *Handler) clearPendingCodexConfirmation(key string, actor string) (bool, bool) {
	h.pendingCodexConfirmsMu.Lock()
	defer h.pendingCodexConfirmsMu.Unlock()
	pending, ok := h.pendingCodexConfirms[key]
	if !ok || pending.message == "" {
		return false, false
	}
	if pending.owner != strings.TrimSpace(actor) {
		return false, true
	}
	delete(h.pendingCodexConfirms, key)
	return true, false
}

func (h *Handler) hasPendingCodexConfirmation() bool {
	return h.pendingCodexConfirmationCount() > 0
}

func (h *Handler) pendingCodexConfirmationCount() int {
	h.pendingCodexConfirmsMu.Lock()
	defer h.pendingCodexConfirmsMu.Unlock()
	return len(h.pendingCodexConfirms)
}
