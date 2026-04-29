package messaging

import "strings"

// SetCodexLocalSessionDir 设置本机 Codex 会话目录，测试和特殊部署可覆盖默认 ~/.codex。
func (h *Handler) SetCodexLocalSessionDir(dir string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.codexLocalSessionDir = strings.TrimSpace(dir)
}

// codexSwitchTargets 合并 WeClaw 已记录会话与本机 Codex 会话，保证编号和 /codex ls 展示一致。
func (h *Handler) codexSwitchTargets(bindingKey string) []codexWorkspaceView {
	storedViews := h.ensureCodexSessions().listWorkspaces(bindingKey)
	views := make([]codexWorkspaceView, 0, len(storedViews))
	seenThreads := make(map[string]bool, len(storedViews))
	for _, view := range storedViews {
		if !isVisibleCodexWorkspace(view) {
			continue
		}
		views = append(views, view)
		if view.ThreadID != "" {
			seenThreads[view.ThreadID] = true
		}
	}
	return h.appendLocalCodexSwitchTargets(views, seenThreads)
}

// appendLocalCodexSwitchTargets 追加未被 WeClaw 记录过的本机会话，避免重复展示同一个 thread。
func (h *Handler) appendLocalCodexSwitchTargets(views []codexWorkspaceView, seenThreads map[string]bool) []codexWorkspaceView {
	for _, view := range h.localCodexSessions() {
		if view.ThreadID == "" || seenThreads[view.ThreadID] {
			continue
		}
		views = append(views, view)
		seenThreads[view.ThreadID] = true
	}
	return views
}

// isVisibleCodexWorkspace 过滤自动登记但尚未产生 thread 的空 workspace。
func isVisibleCodexWorkspace(view codexWorkspaceView) bool {
	return strings.TrimSpace(view.ThreadID) != "" || view.PendingNewThread
}

// localCodexSessions 从当前配置目录读取本机 Codex 会话元数据。
func (h *Handler) localCodexSessions() []codexWorkspaceView {
	h.mu.RLock()
	dir := h.codexLocalSessionDir
	h.mu.RUnlock()
	return discoverLocalCodexSessions(dir)
}

// findLocalCodexWorkspaceByThread 让用户直接按 threadId 切到本机 Codex 已有会话。
func (h *Handler) findLocalCodexWorkspaceByThread(threadID string) (string, bool) {
	threadID = strings.TrimSpace(threadID)
	for _, view := range h.localCodexSessions() {
		if view.ThreadID == threadID {
			return view.WorkspaceRoot, true
		}
	}
	return "", false
}
