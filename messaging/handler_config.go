package messaging

import (
	"strings"
	"time"
)

// SetSaveDir sets the directory for saving images and files.
func (h *Handler) SetSaveDir(dir string) {
	h.saveDir = dir
}

// SetAllowedWorkspaceRoots 设置普通用户 /cwd 允许切换的根目录白名单；空切片表示普通用户禁止远程切换目录。
func (h *Handler) SetAllowedWorkspaceRoots(roots []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cleaned := make([]string, 0, len(roots))
	for _, root := range roots {
		if trimmed := strings.TrimSpace(root); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	h.allowedWorkspaceRoots = cleaned
}

// SetAdminUsers 设置允许执行 WeClaw 管理命令的用户白名单；空白名单表示全部拒绝。
func (h *Handler) SetAdminUsers(users []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.adminUsers = make(map[string]struct{}, len(users))
	for _, user := range users {
		if trimmed := strings.TrimSpace(user); trimmed != "" {
			h.adminUsers[trimmed] = struct{}{}
		}
	}
}

// SetRateLimitPerMinute 设置每用户每分钟触发 agent 的上限；<=0 表示不限流。
func (h *Handler) SetRateLimitPerMinute(limit int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rateLimitPerMinute = limit
	if limit > 0 && h.rateLimiter == nil {
		h.rateLimiter = newUserRateLimiter(time.Minute)
	}
}

// SetAuditLogger 设置审计日志记录器。
func (h *Handler) SetAuditLogger(logger auditLogger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audit = logger
}

func (h *Handler) auditRecord(entry auditEntry) {
	h.mu.RLock()
	logger := h.audit
	h.mu.RUnlock()
	if logger != nil {
		logger.Log(entry)
	}
}

// allowAgentInvocation 在触发 agent 前做每用户限流；返回 false 表示已超限。
func (h *Handler) allowAgentInvocation(routeUserID string) bool {
	h.mu.RLock()
	limit := h.rateLimitPerMinute
	limiter := h.rateLimiter
	h.mu.RUnlock()
	if limit <= 0 || limiter == nil {
		return true
	}
	return limiter.Allow(routeUserID, limit)
}

// isWorkspaceAllowed 判断目标目录是否落在普通用户 /cwd 白名单内；白名单为空时默认拒绝。
func (h *Handler) isWorkspaceAllowed(absPath string) bool {
	h.mu.RLock()
	roots := h.allowedWorkspaceRoots
	h.mu.RUnlock()
	if len(roots) == 0 {
		return false
	}
	return isAllowedAttachmentPath(absPath, roots)
}

// SetCustomAliases sets custom alias mappings from config.
func (h *Handler) SetCustomAliases(aliases map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.customAliases = aliases
}

// SetAgentMetas sets the list of all configured agents (for /status).
func (h *Handler) SetAgentMetas(metas []AgentMeta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentMetas = metas
}

// SetAgentWorkDirs sets the configured working directory for each agent.
func (h *Handler) SetAgentWorkDirs(workDirs map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.agentWorkDirs = make(map[string]string, len(workDirs))
	for name, dir := range workDirs {
		h.agentWorkDirs[name] = dir
	}
}

// SetPlatformDefaultAgents 设置每个平台的默认 Agent 覆盖配置。
func (h *Handler) SetPlatformDefaultAgents(defaults map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.platformDefaultAgents = make(map[string]string, len(defaults))
	for name, agentName := range defaults {
		if trimmed := strings.TrimSpace(agentName); trimmed != "" {
			h.platformDefaultAgents[name] = trimmed
		}
	}
}

// SetCodexAppOpener 设置 Codex App 打开器，主要用于测试外部进程调用。
func (h *Handler) SetCodexAppOpener(opener CodexAppOpener) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.codexAppOpener = opener
}

// SetCodexCLIResumeOpener 设置 Codex CLI resume 打开器，主要用于测试外部进程调用。
func (h *Handler) SetCodexCLIResumeOpener(opener CodexCLIResumeOpener) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.codexCLIResumeOpener = opener
}

// SetClaudeCLIResumeOpener 设置 Claude CLI resume 打开器，主要用于测试外部进程调用。
func (h *Handler) SetClaudeCLIResumeOpener(opener ClaudeCLIResumeOpener) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.claudeCLIResumeOpener = opener
}

// SetServiceAdminCommandExecutor 设置 WeClaw 管理命令执行器，主要用于测试和平台隔离。
func (h *Handler) SetServiceAdminCommandExecutor(executor ServiceAdminCommandExecutor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.serviceAdminExecutor = executor
}
