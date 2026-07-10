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
			lastMod = info.ModTime()
			log.Printf("[config] soft config reloaded from %s", path)
		}
	}
}

// saveDefaultAgent 基于磁盘最新配置更新默认 Agent，避免热重载字段被启动快照覆盖。
func saveDefaultAgent(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.DefaultAgent = name
	return config.Save(cfg)
}

func applySoftConfig(handler *messaging.Handler, registry *platform.Registry, cfg *config.Config) {
	if handler == nil || cfg == nil {
		return
	}
	handler.SetProgressConfig(cfg.Progress)
	handler.SetAgentProgressConfigs(extractAgentProgressConfigs(cfg.Agents))
	handler.SetPlatformProgressConfigs(extractPlatformProgressConfigs(cfg.Platforms))
	handler.SetPlatformDefaultAgents(extractPlatformDefaultAgents(cfg.Platforms))
	handler.SetAllowedWorkspaceRoots(cfg.AllowedWorkspaceRoots)
	handler.SetAdminUsers(cfg.AdminUsers)
	handler.SetRateLimitPerMinute(cfg.RateLimitPerMinute)
	if cfg.DefaultAgent != "" {
		if ag := handler.AgentByName(cfg.DefaultAgent); ag != nil {
			handler.SetDefaultAgent(cfg.DefaultAgent, ag)
		}
	}
	for name, platformConfig := range cfg.Platforms {
		platformName := platform.PlatformName(name)
		if len(platformConfig.Bots) == 0 {
			registry.UpdateAccess(platformName, platformConfig.AllowedUsers)
			continue
		}
		for _, bot := range platformConfig.Bots {
			if !registry.HasAccount(platformName, bot.AppID) {
				log.Printf("[config] %s account %q is configured but not running; restart weclaw to activate new platform account", platformName, bot.AppID)
				continue
			}
			registry.UpdateAccessForAccount(platformName, bot.AppID, bot.AllowedUsers)
		}
	}
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
