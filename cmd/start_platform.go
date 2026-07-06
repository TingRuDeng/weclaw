package cmd

import (
	"fmt"
	"log"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	feishuplatform "github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

func buildPlatformRegistry(accounts []*ilink.Credentials, cfg *config.Config) (*platform.Registry, error) {
	entries := make([]platform.RegistryEntry, 0, len(accounts)+1)
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
	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	if feishuCfg.Enabled != nil && *feishuCfg.Enabled {
		creds, err := feishuplatform.LoadCredentials()
		if err != nil {
			return nil, fmt.Errorf("load feishu credentials: %w", err)
		}
		adapter := feishuplatform.NewAdapter(creds)
		adapter.SetSessionOptions(feishuplatform.FeishuSessionOptions{
			RequireMentionInGroup: feishuCfg.EffectiveRequireMentionInGroup(),
		})
		entries = append(entries, platform.RegistryEntry{
			Platform: adapter,
			Access:   platform.NewAccessControl(feishuCfg.AllowedUsers),
		})
	}
	return platform.NewRegistry(entries), nil
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
