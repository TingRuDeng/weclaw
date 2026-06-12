package messaging

import "strings"

// SetClaudeLocalSessionDir 设置本机 Claude 数据目录，测试和特殊部署可覆盖默认 ~/.claude。
func (h *Handler) SetClaudeLocalSessionDir(dir string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.claudeLocalSessionDir = strings.TrimSpace(dir)
}

// claudeSwitchTargets 合并 WeClaw 已记录会话与本机 Claude 会话。
func (h *Handler) claudeSwitchTargets(bindingKey string) []codexWorkspaceView {
	storedViews := h.ensureClaudeSessions().listWorkspaces(bindingKey)
	views := make([]codexWorkspaceView, 0, len(storedViews))
	seenSessions := make(map[string]bool, len(storedViews))
	for _, view := range storedViews {
		if !isVisibleClaudeWorkspace(view) {
			continue
		}
		views = append(views, view)
		if view.ThreadID != "" {
			seenSessions[view.ThreadID] = true
		}
	}
	return h.appendLocalClaudeSwitchTargets(views, seenSessions)
}

// appendLocalClaudeSwitchTargets 追加未被 WeClaw 记录过的本机会话，避免重复展示同一个 session。
func (h *Handler) appendLocalClaudeSwitchTargets(views []codexWorkspaceView, seenSessions map[string]bool) []codexWorkspaceView {
	for _, view := range h.localClaudeSessions() {
		if view.ThreadID == "" || seenSessions[view.ThreadID] {
			continue
		}
		views = append(views, view)
		seenSessions[view.ThreadID] = true
	}
	return views
}

func isVisibleClaudeWorkspace(view codexWorkspaceView) bool {
	return strings.TrimSpace(view.ThreadID) != "" || view.PendingNewThread
}

func (h *Handler) localClaudeSessions() []codexWorkspaceView {
	h.mu.RLock()
	dir := h.claudeLocalSessionDir
	h.mu.RUnlock()
	return discoverLocalClaudeSessions(dir)
}

func (h *Handler) findLocalClaudeWorkspaceBySession(sessionID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	for _, view := range h.localClaudeSessions() {
		if view.ThreadID == sessionID {
			return view.WorkspaceRoot, true
		}
	}
	return "", false
}
