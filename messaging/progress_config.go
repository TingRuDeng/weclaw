package messaging

import (
	"context"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

// SetProgressConfig 设置全局微信进度反馈配置。
func (h *Handler) SetProgressConfig(cfg config.ProgressConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cfg.Mode == "" {
		cfg = config.DefaultProgressConfig()
	}
	h.progressConfig = cfg
}

// SetAgentProgressConfigs 设置每个 Agent 的进度反馈覆盖配置。
func (h *Handler) SetAgentProgressConfigs(configs map[string]config.ProgressConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentProgressConfigs = make(map[string]config.ProgressConfig, len(configs))
	for name, cfg := range configs {
		h.agentProgressConfigs[name] = cfg
	}
}

// SetPlatformProgressConfigs 设置每个平台的进度反馈覆盖配置。
func (h *Handler) SetPlatformProgressConfigs(configs map[string]config.ProgressConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.platformProgressConfigs = make(map[string]config.ProgressConfig, len(configs))
	for name, cfg := range configs {
		h.platformProgressConfigs[name] = cfg
	}
}

func (h *Handler) resolveProgressConfig(agentName string) config.ProgressConfig {
	return h.resolveProgressConfigForPlatform("", agentName)
}

func (h *Handler) resolveProgressConfigForPlatform(platformName platform.PlatformName, agentName string) config.ProgressConfig {
	return h.resolveProgressConfigForAccount(platformName, "", agentName)
}

func (h *Handler) resolveProgressConfigForAccount(platformName platform.PlatformName, accountID string, agentName string) config.ProgressConfig {
	h.mu.RLock()
	global := h.progressConfig
	override, ok := h.agentProgressConfigs[agentName]
	platformOverride, platformOK := h.platformProgressConfigs[string(platformName)]
	accountOverride, accountOK := h.platformProgressConfigs[PlatformAccountConfigKey(platformName, accountID)]
	h.mu.RUnlock()
	if global.Mode == "" {
		global = config.DefaultProgressConfig()
	}
	if ok {
		global = config.NormalizeProgressConfig(global, &override)
	}
	if platformOK {
		global = config.NormalizeProgressConfig(global, &platformOverride)
	}
	if accountOK {
		global = config.NormalizeProgressConfig(global, &accountOverride)
	}
	return normalizePlatformProgressConfig(platformName, global, platformOK || accountOK)
}

// normalizePlatformProgressConfig 收敛平台默认进度体验，避免把不完整的 Agent delta 暴露给终端用户。
func normalizePlatformProgressConfig(platformName platform.PlatformName, cfg config.ProgressConfig, hasPlatformOverride bool) config.ProgressConfig {
	if platformName != platform.PlatformFeishu || hasPlatformOverride {
		return cfg
	}
	if cfg.Mode == progressModeOff {
		return cfg
	}
	cfg.Mode = progressModeSummary
	return cfg
}

func (h *Handler) defaultAgentNameForPlatform(platformName platform.PlatformName) string {
	return h.defaultAgentNameForAccount(platformName, "")
}

func (h *Handler) defaultAgentNameForAccount(platformName platform.PlatformName, accountID string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if agentName := h.platformDefaultAgents[PlatformAccountConfigKey(platformName, accountID)]; agentName != "" {
		return agentName
	}
	if agentName := h.platformDefaultAgents[string(platformName)]; agentName != "" {
		return agentName
	}
	return h.defaultName
}

// PlatformAccountConfigKey 构造平台账号级配置 key，用于多飞书机器人隔离默认 Agent 和进度配置。
func PlatformAccountConfigKey(platformName platform.PlatformName, accountID string) string {
	if platformName == "" || accountID == "" {
		return ""
	}
	return string(platformName) + "\x00" + accountID
}

// contextWithTaskTimeout 只限制 Agent 执行耗时，最终失败回复继续使用原始请求上下文发送。
func contextWithTaskTimeout(ctx context.Context, cfg config.ProgressConfig) (context.Context, context.CancelFunc) {
	if cfg.TaskTimeoutSeconds <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(cfg.TaskTimeoutSeconds)*time.Second)
}
