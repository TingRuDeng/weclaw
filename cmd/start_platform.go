package cmd

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	feishuplatform "github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

func buildPlatformRegistry(accounts []*ilink.Credentials, cfg *config.Config) (*platform.Registry, error) {
	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	entries := make([]platform.RegistryEntry, 0, len(accounts)+len(feishuCfg.Bots))
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	if !wechatEnabled(cfg) {
		log.Printf("[platform] wechat disabled by config")
	} else {
		for _, creds := range accounts {
			adapter := wechat.NewAdapter(creds)
			adapter.SetAggregationWindow(wechatAggregationWindow(wechatCfg))
			entries = append(entries, platform.RegistryEntry{
				Platform: adapter,
				Access:   platform.NewAccessControl(wechatCfg.AllowedUsers),
			})
		}
	}
	if feishuCfg.Enabled != nil && *feishuCfg.Enabled {
		feishuEntries, err := buildFeishuRegistryEntries(feishuCfg)
		if err != nil {
			return nil, err
		}
		entries = append(entries, feishuEntries...)
	}
	return platform.NewRegistry(entries), nil
}

func buildFeishuRegistryEntries(feishuCfg config.PlatformConfig) ([]platform.RegistryEntry, error) {
	if len(feishuCfg.Bots) == 0 {
		return nil, fmt.Errorf("platforms.feishu.bots is required when feishu is enabled")
	}
	entries := make([]platform.RegistryEntry, 0, len(feishuCfg.Bots))
	for _, bot := range feishuCfg.Bots {
		entry, err := buildFeishuRegistryEntry(bot)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func buildFeishuRegistryEntry(bot config.FeishuBotConfig) (platform.RegistryEntry, error) {
	creds, err := feishuplatform.LoadCredentialsForBot(bot.Name)
	if err != nil {
		return platform.RegistryEntry{}, fmt.Errorf("load feishu credentials for %q: %w", bot.Name, err)
	}
	if strings.TrimSpace(creds.AppID) != strings.TrimSpace(bot.AppID) {
		return platform.RegistryEntry{}, fmt.Errorf("feishu bot %q app_id mismatch: config %q, credentials %q", bot.Name, bot.AppID, creds.AppID)
	}
	adapter := feishuplatform.NewAdapter(creds)
	adapter.SetSessionOptions(feishuplatform.FeishuSessionOptions{
		RequireMentionInGroup: bot.EffectiveRequireMentionInGroup(),
	})
	return platform.RegistryEntry{
		Platform: adapter,
		Access:   platform.NewAccessControl(bot.AllowedUsers),
	}, nil
}

func wechatEnabled(cfg *config.Config) bool {
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	return wechatCfg.Enabled == nil || *wechatCfg.Enabled
}

func wechatAggregationWindow(cfg config.PlatformConfig) time.Duration {
	if cfg.MessageAggregationMs == nil {
		return 800 * time.Millisecond
	}
	if *cfg.MessageAggregationMs <= 0 {
		return 0
	}
	return time.Duration(*cfg.MessageAggregationMs) * time.Millisecond
}
