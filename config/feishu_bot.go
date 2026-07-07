package config

import (
	"fmt"
	"strings"
)

// FeishuBotDisplayName 返回面向用户展示的机器人名称，未配置时回退到内部 ID。
func FeishuBotDisplayName(bot FeishuBotConfig) string {
	if displayName := strings.TrimSpace(bot.DisplayName); displayName != "" {
		return displayName
	}
	return strings.TrimSpace(bot.Name)
}

// FeishuBotReferences 返回可用于引用机器人的全部名称，供 CLI 做中文别名解析。
func FeishuBotReferences(bot FeishuBotConfig) []string {
	refs := make([]string, 0, len(bot.Aliases)+2)
	refs = appendUniqueBotRef(refs, bot.Name)
	refs = appendUniqueBotRef(refs, bot.DisplayName)
	for _, alias := range bot.Aliases {
		refs = appendUniqueBotRef(refs, alias)
	}
	return refs
}

func validateFeishuBotReferences(bots []FeishuBotConfig) error {
	owners := make(map[string]string)
	for _, bot := range bots {
		owner := strings.TrimSpace(bot.Name)
		for _, ref := range FeishuBotReferences(bot) {
			if existing := owners[ref]; existing != "" && existing != owner {
				return fmt.Errorf("duplicate feishu bot alias %q", ref)
			}
			owners[ref] = owner
		}
	}
	return nil
}

func appendUniqueBotRef(refs []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return refs
	}
	for _, ref := range refs {
		if ref == value {
			return refs
		}
	}
	return append(refs, value)
}
