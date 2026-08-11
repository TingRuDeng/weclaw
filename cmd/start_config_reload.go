package cmd

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/platform"
)

func extractAgentProgressConfigs(agents map[string]config.AgentConfig) map[string]config.ProgressConfig {
	progressConfigs := make(map[string]config.ProgressConfig)
	for name, agentConfig := range agents {
		if agentConfig.Progress == nil {
			continue
		}
		progressConfigs[name] = *agentConfig.Progress
	}
	return progressConfigs
}

func extractPlatformProgressConfigs(platforms map[string]config.PlatformConfig) map[string]config.ProgressConfig {
	progressConfigs := make(map[string]config.ProgressConfig)
	for name, platformConfig := range platforms {
		if platformConfig.Progress == nil {
			addBotProgressConfigs(progressConfigs, platform.PlatformName(name), platformConfig.Bots)
			continue
		}
		progressConfigs[name] = *platformConfig.Progress
		addBotProgressConfigs(progressConfigs, platform.PlatformName(name), platformConfig.Bots)
	}
	return progressConfigs
}

func extractPlatformDefaultAgents(platforms map[string]config.PlatformConfig) map[string]string {
	defaultAgents := make(map[string]string)
	for name, platformConfig := range platforms {
		if platformConfig.DefaultAgent != "" {
			defaultAgents[name] = platformConfig.DefaultAgent
		}
		addBotDefaultAgents(defaultAgents, platform.PlatformName(name), platformConfig.Bots)
	}
	return defaultAgents
}

func runSoftConfigReloader(ctx context.Context, handler *messaging.Handler, registry *platform.Registry) {
	path, err := config.ConfigPath()
	if err != nil {
		log.Printf("[config] WARNING: cannot resolve config path for hot reload: %v", err)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	lastMod := info.ModTime()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil || !info.ModTime().After(lastMod) {
				continue
			}
			next, err := config.Load()
			if err != nil {
				log.Printf("[config] WARNING: hot reload failed, keeping previous config: %v", err)
				lastMod = info.ModTime()
				continue
			}
			applySoftConfig(handler, registry, next)
			if err := applyLatestPlatformAccess(registry); err != nil {
				log.Printf("[config] WARNING: platform access hot reload failed, keeping previous access: %v", err)
				continue
			}
			lastMod = info.ModTime()
			log.Printf("[config] soft config reloaded from %s", path)
		}
	}
}

// saveDefaultAgent 基于磁盘最新配置更新默认 Agent，避免热重载字段被启动快照覆盖。
func saveDefaultAgent(name string) error {
	return config.Update(func(cfg *config.Config) error {
		cfg.DefaultAgent = name
		return nil
	})
}

func applySoftConfig(handler *messaging.Handler, _ *platform.Registry, cfg *config.Config) {
	if handler == nil || cfg == nil {
		return
	}
	handler.SetProgressConfig(cfg.Progress)
	handler.SetAgentProgressConfigs(extractAgentProgressConfigs(cfg.Agents))
	handler.SetPlatformProgressConfigs(extractPlatformProgressConfigs(cfg.Platforms))
	handler.SetPlatformDefaultAgents(extractPlatformDefaultAgents(cfg.Platforms))
	handler.SetAllowedWorkspaceRoots(cfg.AllowedWorkspaceRoots)
	handler.SetRateLimitPerMinute(cfg.RateLimitPerMinute)
	if cfg.DefaultAgent != "" {
		if ag := handler.AgentByName(cfg.DefaultAgent); ag != nil {
			handler.SetDefaultAgent(cfg.DefaultAgent, ag)
		}
	}
}

func applyLatestPlatformAccess(registry *platform.Registry) error {
	if registry == nil {
		return nil
	}
	return config.WithLockedSnapshot(func(latest *config.Config) error {
		applyPlatformAccessConfig(registry, latest.Platforms)
		return nil
	})
}

func latestPlatformAccessUpdater(registry *platform.Registry) func(platform.PlatformName, string, []string) {
	return func(platform.PlatformName, string, []string) {
		if err := applyLatestPlatformAccess(registry); err != nil {
			log.Printf("[config] WARNING: immediate platform access refresh failed: %v", err)
		}
	}
}

func applyPlatformAccessConfig(registry *platform.Registry, platforms map[string]config.PlatformConfig) {
	if registry == nil {
		return
	}
	for _, account := range registry.RegisteredAccounts() {
		allowed, configured := configuredPlatformAccountAccess(platforms, account)
		if !configured {
			allowed = nil
		}
		registry.UpdateAccessForAccount(account.Platform, account.AccountID, allowed)
	}
	for name, platformConfig := range platforms {
		platformName := platform.PlatformName(name)
		if !platformAccessEnabled(platforms, platformName, platformConfig) {
			continue
		}
		for _, bot := range platformConfig.Bots {
			if registry.HasAccount(platformName, bot.AppID) {
				continue
			}
			log.Printf("[config] %s account %q is configured but not running; restart weclaw to activate new platform account", platformName, bot.AppID)
		}
	}
}

func configuredPlatformAccountAccess(
	platforms map[string]config.PlatformConfig,
	account platform.RegistryAccount,
) ([]string, bool) {
	platformConfig, exists := platforms[string(account.Platform)]
	if !exists || !platformAccessEnabled(platforms, account.Platform, platformConfig) {
		return nil, false
	}
	if len(platformConfig.Bots) == 0 {
		return platformConfig.AllowedUsers, true
	}
	for _, bot := range platformConfig.Bots {
		if bot.AppID == account.AccountID {
			return bot.AllowedUsers, true
		}
	}
	return nil, false
}

func platformAccessEnabled(
	platforms map[string]config.PlatformConfig,
	platformName platform.PlatformName,
	platformConfig config.PlatformConfig,
) bool {
	if platformName == platform.PlatformFeishu {
		return platformConfig.Enabled != nil && *platformConfig.Enabled
	}
	if platformName == platform.PlatformWeChat {
		if platformConfig.Enabled != nil {
			return *platformConfig.Enabled
		}
		feishuConfig := platforms[string(platform.PlatformFeishu)]
		return feishuConfig.Enabled == nil || !*feishuConfig.Enabled
	}
	return platformConfig.Enabled == nil || *platformConfig.Enabled
}

func addBotProgressConfigs(target map[string]config.ProgressConfig, platformName platform.PlatformName, bots []config.FeishuBotConfig) {
	for _, bot := range bots {
		if bot.Progress == nil {
			continue
		}
		target[messaging.PlatformAccountConfigKey(platformName, bot.AppID)] = *bot.Progress
	}
}

func addBotDefaultAgents(target map[string]string, platformName platform.PlatformName, bots []config.FeishuBotConfig) {
	for _, bot := range bots {
		if bot.DefaultAgent == "" {
			continue
		}
		target[messaging.PlatformAccountConfigKey(platformName, bot.AppID)] = bot.DefaultAgent
	}
}
