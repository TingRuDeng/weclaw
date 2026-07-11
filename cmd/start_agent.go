package cmd

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

var codexACPStartupRetryDelay = 2 * time.Second

// createAgentByName creates and starts an agent by its config name.
// Returns nil if the agent is not configured or fails to start.
func createAgentByName(ctx context.Context, cfg *config.Config, name string) agent.Agent {
	agCfg, ok := cfg.Agents[name]
	if !ok {
		log.Printf("[agent] %q not found in config", name)
		return nil
	}

	switch agCfg.Type {
	case "acp":
		ag, err := startACPAgentWithRetry(ctx, name, agCfg)
		if err != nil {
			log.Printf("[agent] failed to start ACP agent %q: %v", name, err)
			return nil
		}
		log.Printf("[agent] started ACP agent: %s (command=%s, type=%s, model=%s, effort=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model, agCfg.Effort)
		return ag
	case "cli":
		ag := agent.NewCLIAgent(agent.CLIAgentConfig{
			Name:         name,
			Command:      agCfg.Command,
			Args:         agCfg.Args,
			Cwd:          agCfg.Cwd,
			Env:          agCfg.Env,
			Model:        agCfg.Model,
			Effort:       agCfg.Effort,
			SystemPrompt: agCfg.SystemPrompt,
			RunAsUser:    agCfg.RunAsUser,
			RunAsEnv:     agCfg.RunAsEnv,
		})
		log.Printf("[agent] created CLI agent: %s (command=%s, type=%s, model=%s, effort=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model, agCfg.Effort)
		return ag
	case "http":
		if agCfg.Endpoint == "" {
			log.Printf("[agent] HTTP agent %q has no endpoint", name)
			return nil
		}
		ag, err := agent.NewHTTPAgent(agent.HTTPAgentConfig{
			Endpoint:     agCfg.Endpoint,
			APIKey:       agCfg.APIKey,
			Headers:      agCfg.Headers,
			Model:        agCfg.Model,
			SystemPrompt: agCfg.SystemPrompt,
			MaxHistory:   agCfg.MaxHistory,
		})
		if err != nil {
			log.Printf("[agent] invalid HTTP agent %q config: %v", name, err)
			return nil
		}
		log.Printf("[agent] created HTTP agent: %s (endpoint=%s, model=%s)", name, agCfg.Endpoint, agCfg.Model)
		return ag
	case "companion":
		if agCfg.Command == "" {
			log.Printf("[agent] companion agent %q has no command", name)
			return nil
		}
		ag := agent.NewCompanionAgent(agent.CompanionAgentConfig{
			Name:       name,
			Command:    agCfg.Command,
			Args:       agCfg.Args,
			Cwd:        agCfg.Cwd,
			Env:        agCfg.Env,
			Model:      agCfg.Model,
			AutoLaunch: companionAutoLaunchEnabled(name, agCfg),
		})
		if err := ag.Start(ctx); err != nil {
			log.Printf("[agent] failed to start companion agent %q: %v", name, err)
			return nil
		}
		log.Printf("[agent] started companion agent: %s (command=%s, type=%s)", name, agCfg.Command, agCfg.Type)
		return ag
	default:
		log.Printf("[agent] unknown type %q for %q", agCfg.Type, name)
		return nil
	}
}

func startACPAgentWithRetry(ctx context.Context, name string, agCfg config.AgentConfig) (*agent.ACPAgent, error) {
	if err := agCfg.ValidateCodexPermissionConfig(); err != nil {
		return nil, err
	}
	attempts := 1
	if isCodexAppServerAgent(agCfg) {
		attempts = 3
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ag := newACPAgentFromConfig(agCfg)
		if err := ag.Start(ctx); err != nil {
			lastErr = err
			if attempt == attempts || !isRetryableCodexStateRuntimeError(err) {
				return nil, err
			}
			log.Printf("[agent] retrying Codex ACP startup after sqlite state runtime error (agent=%s, attempt=%d/%d): %v", name, attempt+1, attempts, err)
			if err := sleepContext(ctx, codexACPStartupRetryDelay); err != nil {
				return nil, err
			}
			continue
		}
		return ag, nil
	}
	return nil, lastErr
}

func newACPAgentFromConfig(agCfg config.AgentConfig) *agent.ACPAgent {
	return agent.NewACPAgent(agent.ACPAgentConfig{
		Command:          agCfg.Command,
		Args:             agCfg.Args,
		Cwd:              agCfg.Cwd,
		Env:              agCfg.Env,
		Model:            agCfg.Model,
		Effort:           agCfg.Effort,
		ApprovalPolicy:   agCfg.EffectiveApprovalPolicy(),
		ApprovalReviewer: agCfg.EffectiveApprovalReviewer(),
		SandboxMode:      agCfg.EffectiveSandboxMode(),
		SystemPrompt:     agCfg.SystemPrompt,
		RunAsUser:        agCfg.RunAsUser,
		RunAsEnv:         agCfg.RunAsEnv,
	})
}

func isCodexAppServerAgent(agCfg config.AgentConfig) bool {
	if filepath.Base(agCfg.Command) != "codex" {
		return false
	}
	for _, arg := range agCfg.Args {
		if arg == "app-server" {
			return true
		}
	}
	return false
}

func isRetryableCodexStateRuntimeError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "failed to initialize sqlite state runtime") ||
		strings.Contains(text, "failed to initialize state runtime")
}

func companionAutoLaunchEnabled(_ string, agCfg config.AgentConfig) bool {
	if agCfg.AutoLaunch != nil {
		return *agCfg.AutoLaunch
	}
	return false
}
