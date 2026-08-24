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

type platformSelection struct {
	wechat bool
	feishu bool
}

// resolvePlatformSelection 兼容未写 enabled 的旧微信配置，同时让全新配置保持未选择状态。
func resolvePlatformSelection(cfg *config.Config, wechatAccountCount int) platformSelection {
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	selection := platformSelection{
		feishu: feishuCfg.Enabled != nil && *feishuCfg.Enabled,
	}
	if wechatCfg.Enabled != nil {
		selection.wechat = *wechatCfg.Enabled
		return selection
	}
	selection.wechat = !selection.feishu && wechatAccountCount > 0
	return selection
}

func needsWechatAccounts(cfg *config.Config) bool {
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	if wechatCfg.Enabled != nil {
		return *wechatCfg.Enabled
	}
	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	return feishuCfg.Enabled == nil || !*feishuCfg.Enabled
}

func persistLegacyWeChatSelection(cfg *config.Config, accountCount int, update func(func(*config.Config) error) error) error {
	if cfg == nil || accountCount == 0 || cfg.Platforms[string(platform.PlatformWeChat)].Enabled != nil {
		return nil
	}
	if feishu := cfg.Platforms[string(platform.PlatformFeishu)]; feishu.Enabled != nil && *feishu.Enabled {
		return nil
	}
	if update == nil {
		return fmt.Errorf("persist legacy WeChat selection: config updater is nil")
	}
	var latestWechat config.PlatformConfig
	var latestFeishu config.PlatformConfig
	if err := update(func(latest *config.Config) error {
		latestWechat = latest.Platforms[string(platform.PlatformWeChat)]
		latestFeishu = latest.Platforms[string(platform.PlatformFeishu)]
		if latestWechat.Enabled != nil || (latestFeishu.Enabled != nil && *latestFeishu.Enabled) {
			return nil
		}
		enabled := true
		latestWechat.Enabled = &enabled
		latest.Platforms[string(platform.PlatformWeChat)] = latestWechat
		return nil
	}); err != nil {
		return fmt.Errorf("persist legacy WeChat selection: %w", err)
	}
	cfg.Platforms[string(platform.PlatformWeChat)] = latestWechat
	cfg.Platforms[string(platform.PlatformFeishu)] = latestFeishu
	return nil
}

func buildPlatformRegistry(accounts []*ilink.Credentials, cfg *config.Config, opts ...platform.RegistryOption) (*platform.Registry, error) {
	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	entries := make([]platform.RegistryEntry, 0, len(accounts)+len(feishuCfg.Bots))
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	selection := resolvePlatformSelection(cfg, len(accounts))
	if !selection.wechat {
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
	if selection.feishu {
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
	dedupStateFile, err := feishuDedupStateFile(creds.AppID)
	if err != nil {
		return platform.RegistryEntry{}, fmt.Errorf("resolve feishu dedup state for %q: %w", bot.Name, err)
	}
	adapter.SetDedupStateFile(dedupStateFile)
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

func wechatAggregationWindow(cfg config.PlatformConfig) time.Duration {
	if cfg.MessageAggregationMs == nil {
		return 800 * time.Millisecond
	}
	if *cfg.MessageAggregationMs <= 0 {
		return 0
	}
	return time.Duration(*cfg.MessageAggregationMs) * time.Millisecond
}

func feishuDedupStateFile(appID string) (string, error) {
	name := strings.Trim(feishuStateFileUnsafeChars.ReplaceAllString(strings.TrimSpace(appID), "-"), "-")
	if name == "" {
		name = "default"
	}
	return resolveWeclawFile(filepath.Join("state", "feishu-dedup-"+name+".json"))
}
