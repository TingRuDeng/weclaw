package messaging

import (
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

const auditErrorLogInterval = time.Minute

// SetSaveDir sets the directory for saving images and files.
func (h *Handler) SetSaveDir(dir string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.saveDir = dir
}

// saveDirectory 返回当前保存目录快照，避免在消息处理期间持有配置锁。
func (h *Handler) saveDirectory() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.saveDir
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
		if err := logger.Log(entry); err != nil {
			h.auditErrorMu.Lock()
			now := time.Now()
			shouldLog := h.lastAuditErrorAt.IsZero() || now.Sub(h.lastAuditErrorAt) >= auditErrorLogInterval
			if shouldLog {
				h.lastAuditErrorAt = now
			}
			h.auditErrorMu.Unlock()
			if shouldLog {
				log.Printf("[audit] record failed: %v", err)
			}
		}
	}
}

// allowAgentInvocation 在触发 agent 前做每用户限流；返回 false 表示已超限。
func (h *Handler) allowAgentInvocation(platformName platform.PlatformName, accountID string, userID string) bool {
	h.mu.RLock()
	limit := h.rateLimitPerMinute
	limiter := h.rateLimiter
	h.mu.RUnlock()
	if limit <= 0 || limiter == nil {
		return true
	}
	return limiter.Allow(agentRateLimitKey(platformName, accountID, userID), limit)
}

func agentRateLimitKey(platformName platform.PlatformName, accountID string, userID string) string {
	if strings.TrimSpace(userID) == "" {
		return ""
	}
	return strings.TrimSpace(string(platformName)) + "\x00" + strings.TrimSpace(accountID) + "\x00" + strings.TrimSpace(userID)
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

// SetServiceAdminCommandExecutor 设置 WeClaw 管理命令执行器，主要用于测试和平台隔离。
func (h *Handler) SetServiceAdminCommandExecutor(executor ServiceAdminCommandExecutor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.serviceAdminExecutor = executor
}
