package messaging

// sessionService 收拢 Handler 的会话状态仓库归属；各 store 继续自行维护锁和持久化事务。
type sessionService struct {
	agent  *agentSessionStore
	codex  *codexSessionStore
	claude *claudeSessionStore
}

func newSessionService() *sessionService {
	return &sessionService{
		agent:  newAgentSessionStore(),
		codex:  newCodexSessionStore(),
		claude: newClaudeSessionStore(),
	}
}

func (h *Handler) ensureSessionService() *sessionService {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions == nil {
		h.sessions = newSessionService()
	}
	return h.sessions
}

func (h *Handler) ensureAgentSessions() *agentSessionStore {
	return h.ensureSessionService().agent
}

// SetAgentSessionFile 设置会话级默认 Agent 的持久化文件。
func (h *Handler) SetAgentSessionFile(filePath string) error {
	return h.ensureAgentSessions().SetFilePath(filePath)
}

func (h *Handler) ensureCodexSessions() *codexSessionStore {
	return h.ensureSessionService().codex
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
	return h.ensureSessionService().claude
}

// SetClaudeSessionFile 设置 Claude workspace/session 列表的持久化文件。
func (h *Handler) SetClaudeSessionFile(filePath string) error {
	return h.ensureClaudeSessions().SetFilePath(filePath)
}
