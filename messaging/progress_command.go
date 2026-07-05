package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

func (h *Handler) handleProgressCommand(trimmed string) string {
	return h.handleProgressCommandForPlatform(trimmed, "")
}

func (h *Handler) handleProgressCommandForPlatform(trimmed string, platformName platform.PlatformName) string {
	fields := strings.Fields(trimmed)
	if len(fields) == 1 {
		return wechatCommandText(
			"当前进度模式："+h.resolveProgressConfigForProgressCommand(platformName).Mode,
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

	cfg := h.resolveProgressConfigForProgressCommand(platformName)
	cfg.Mode = mode
	h.setProgressConfigForProgressCommand(platformName, cfg)
	return "已切换进度模式：" + mode
}

func (h *Handler) resolveProgressConfigForProgressCommand(platformName platform.PlatformName) config.ProgressConfig {
	if platformName == "" {
		return h.resolveProgressConfig("")
	}
	return h.resolveProgressConfigForPlatform(platformName, "")
}

func (h *Handler) setProgressConfigForProgressCommand(platformName platform.PlatformName, cfg config.ProgressConfig) {
	if platformName == "" {
		h.SetProgressConfig(cfg)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.platformProgressConfigs == nil {
		h.platformProgressConfigs = make(map[string]config.ProgressConfig)
	}
	h.platformProgressConfigs[string(platformName)] = cfg
}
