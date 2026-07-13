package cmd

import (
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	feishuplatform "github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

var feishuStateFileUnsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func buildPlatformRegistry(accounts []*ilink.Credentials, cfg *config.Config, opts ...platform.RegistryOption) (*platform.Registry, error) {
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
	return platform.NewRegistry(entries, opts...), nil
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
	log.Printf("[platform] registering feishu bot name=%s display=%s account=%s", bot.Name, config.FeishuBotDisplayName(bot), bot.AppID)
	adapter := feishuplatform.NewAdapter(creds)
	adapter.SetMaxMessageAge(resolveFeishuMaxMessageAge(bot))
	adapter.SetDedupStateFile(feishuDedupStateFile(creds.AppID))
	adapter.SetSessionOptions(feishuplatform.FeishuSessionOptions{
		RequireMentionInGroup: bot.EffectiveRequireMentionInGroup(),
	})
	return platform.RegistryEntry{
		Platform: adapter,
		Access:   platform.NewAccessControl(bot.AllowedUsers),
	}, nil
}

// resolveFeishuMaxMessageAge 返回单个飞书机器人使用的消息时效窗口。
func resolveFeishuMaxMessageAge(bot config.FeishuBotConfig) time.Duration {
	if bot.MaxMessageAgeSeconds == nil {
		return feishuplatform.DefaultMessageMaxAge
	}
	return time.Duration(*bot.MaxMessageAgeSeconds) * time.Second
}

func wechatEnabled(cfg *config.Config) bool {
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	if wechatCfg.Enabled != nil {
		return *wechatCfg.Enabled
	}
	// 飞书-only 新用户没有微信账号时，启动不能被微信自动登录阻塞。
	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	return feishuCfg.Enabled == nil || !*feishuCfg.Enabled
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

func feishuDedupStateFile(appID string) string {
	name := strings.Trim(feishuStateFileUnsafeChars.ReplaceAllString(strings.TrimSpace(appID), "-"), "-")
	if name == "" {
		name = "default"
	}
	return filepath.Join(weclawDir(), "state", "feishu-dedup-"+name+".json")
}
