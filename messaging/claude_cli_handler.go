package messaging

import "strings"

// handleClaudeCLI is intentionally fail-closed. A native `claude --resume`
// process would be a second writer outside the shared ClaudeHost lease.
func (h *Handler) handleClaudeCLI(_ claudeSessionRoute) string {
	return claudeSingleHostEntryDisabled("cli")
}

// hasActiveClaudeTask reports whether this frontend currently owns the active
// writer lease for its bound session. A different frontend using the same
// session does not block this frontend from changing its own binding.
func (h *Handler) hasActiveClaudeTask(route claudeSessionRoute, _ string) bool {
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	return h.hasActiveClaudeBindingSession(route, binding.SessionID)
}

func (h *Handler) hasActiveClaudeBindingSession(route claudeSessionRoute, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	task, active := h.activeTask(claudeSessionExecutionKey(sessionID))
	if !active {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.routeUserID == strings.TrimSpace(route.UserID)
}
