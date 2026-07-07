package cmd

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
)

// resolveFeishuBotName 将用户输入的内部 ID、展示名或别名解析为稳定的 bot name。
func resolveFeishuBotName(ref string) (string, error) {
	bot, matched, err := resolveFeishuBotRef(ref)
	if err != nil {
		return "", err
	}
	if matched {
		return bot.Name, nil
	}
	return strings.TrimSpace(ref), nil
}

func resolveFeishuBotRef(ref string) (config.FeishuBotConfig, bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return config.FeishuBotConfig{}, false, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return config.FeishuBotConfig{}, false, err
	}
	for _, bot := range cfg.Platforms["feishu"].Bots {
		if feishuBotMatchesRef(bot, ref) {
			return bot, true, nil
		}
	}
	return config.FeishuBotConfig{}, false, nil
}

func feishuBotMatchesRef(bot config.FeishuBotConfig, ref string) bool {
	for _, candidate := range config.FeishuBotReferences(bot) {
		if candidate == ref {
			return true
		}
	}
	return false
}

func feishuBotDisplayLabel(bot config.FeishuBotConfig) string {
	displayName := config.FeishuBotDisplayName(bot)
	if displayName == strings.TrimSpace(bot.Name) {
		return displayName
	}
	return displayName + " (" + strings.TrimSpace(bot.Name) + ")"
}
