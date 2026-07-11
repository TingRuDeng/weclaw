package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

func (h *Handler) handleProgressCommand(trimmed string) string {
	return h.handleProgressCommandForAccount(trimmed, "", "")
}

func (h *Handler) handleProgressCommandForAccount(trimmed string, platformName platform.PlatformName, accountID string) string {
	fields := strings.Fields(trimmed)
	if len(fields) == 1 {
		return wechatCommandText(
			"当前进度模式："+h.resolveProgressConfigForProgressCommand(platformName, accountID).Mode,
			"可用模式：off、typing、summary、verbose、stream、debug",
		)
	}
	if len(fields) != 2 {
		return "用法：/progress 或 /progress <off|typing|summary|verbose|stream|debug>"
	}

	mode := fields[1]
	if !isSupportedProgressMode(mode) {
		return wechatCommandText(
			"不支持的进度模式："+mode,
			"可用模式：off、typing、summary、verbose、stream、debug",
		)
	}

	cfg := h.resolveProgressConfigForProgressCommand(platformName, accountID)
	cfg.Mode = mode
	h.setProgressConfigForProgressCommand(platformName, accountID, cfg)
	return "已切换进度模式：" + mode
}

func (h *Handler) resolveProgressConfigForProgressCommand(platformName platform.PlatformName, accountID string) config.ProgressConfig {
	if platformName == "" {
		return h.resolveProgressConfig("")
	}
	return h.resolveProgressConfigForAccount(platformName, accountID, "")
}

func (h *Handler) setProgressConfigForProgressCommand(platformName platform.PlatformName, accountID string, cfg config.ProgressConfig) {
	if platformName == "" {
		h.SetProgressConfig(cfg)
		return
	}
	key := string(platformName)
	if accountKey := PlatformAccountConfigKey(platformName, accountID); accountKey != "" {
		key = accountKey
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.platformProgressConfigs == nil {
		h.platformProgressConfigs = make(map[string]config.ProgressConfig)
	}
	h.platformProgressConfigs[key] = cfg
}
