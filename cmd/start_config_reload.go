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
			continue
		}
		progressConfigs[name] = *platformConfig.Progress
	}
	return progressConfigs
}

func extractPlatformDefaultAgents(platforms map[string]config.PlatformConfig) map[string]string {
	defaultAgents := make(map[string]string)
	for name, platformConfig := range platforms {
		if platformConfig.DefaultAgent == "" {
			continue
		}
		defaultAgents[name] = platformConfig.DefaultAgent
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

func applySoftConfig(handler *messaging.Handler, registry *platform.Registry, cfg *config.Config) {
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
	for name, platformConfig := range cfg.Platforms {
		registry.UpdateAccess(platform.PlatformName(name), platformConfig.AllowedUsers)
	}
}
