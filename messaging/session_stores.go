package messaging

func (h *Handler) ensureCodexSessions() *codexSessionStore {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.codexSessions == nil {
		h.codexSessions = newCodexSessionStore()
	}
	return h.codexSessions
}

// SetCodexSessionFile 设置 Codex workspace/thread 列表的持久化文件。
func (h *Handler) SetCodexSessionFile(filePath string) {
	h.ensureCodexSessions().SetFilePath(filePath)
}

func (h *Handler) ensureFeishuIdentities() *feishuIdentityStore {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.feishuIdentities == nil {
		h.feishuIdentities = newFeishuIdentityStore()
	}
	return h.feishuIdentities
}

// SetFeishuIdentityFile 设置飞书自动发现身份的持久化文件。
func (h *Handler) SetFeishuIdentityFile(filePath string) {
	h.ensureFeishuIdentities().SetFilePath(filePath)
}

func (h *Handler) ensureClaudeSessions() *claudeSessionStore {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.claudeSessions == nil {
		h.claudeSessions = newClaudeSessionStore()
	}
	return h.claudeSessions
}

// SetClaudeSessionFile 设置 Claude workspace/session 列表的持久化文件。
func (h *Handler) SetClaudeSessionFile(filePath string) {
	h.ensureClaudeSessions().SetFilePath(filePath)
}
