package cmd

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
)

var codexACPStartupRetryDelay = 2 * time.Second

// createAgentByName 按配置名称创建 Agent；配置缺失或启动失败时返回 nil。
func createAgentByName(ctx context.Context, cfg *config.Config, name string, protocolTrace ...observability.ProtocolRecorder) agent.Agent {
	agCfg, ok := cfg.Agents[name]
	if !ok {
		log.Printf("[agent] %q not found in config", name)
		return nil
	}
	if name == "claude" && agCfg.Type != "acp" {
		log.Printf("[agent] Claude remote backend only supports ACP; run weclaw config agent")
		return nil
	}

	switch agCfg.Type {
	case "acp":
		return createACPAgent(ctx, name, agCfg, protocolTrace...)
	case "cli":
		return createCLIAgent(name, agCfg)
	case "http":
		return createHTTPAgent(name, agCfg)
	case "companion":
		return createCompanionAgent(ctx, name, agCfg)
	default:
		log.Printf("[agent] unknown type %q for %q", agCfg.Type, name)
		return nil
	}
}

// createACPAgent 启动带重试策略的 ACP Agent。
func createACPAgent(ctx context.Context, name string, agCfg config.AgentConfig, protocolTrace ...observability.ProtocolRecorder) agent.Agent {
	ag, err := startACPAgentWithRetry(ctx, name, agCfg, protocolTrace...)
	if err != nil {
		log.Printf("[agent] failed to start ACP agent %q: %v", name, err)
		return nil
	}
	log.Printf("[agent] started ACP agent: %s (command=%s, type=%s, model=%s, effort=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model, agCfg.Effort)
	return ag
}

// createCLIAgent 创建按次调用的 CLI Agent。
func createCLIAgent(name string, agCfg config.AgentConfig) agent.Agent {
	ag := agent.NewCLIAgent(agent.CLIAgentConfig{
		Name: name, Command: agCfg.Command, Args: agCfg.Args, Cwd: agCfg.Cwd,
		Env: agCfg.Env, Model: agCfg.Model, Effort: agCfg.Effort,
		SystemPrompt: agCfg.SystemPrompt, RunAsUser: agCfg.RunAsUser, RunAsEnv: agCfg.RunAsEnv,
	})
	log.Printf("[agent] created CLI agent: %s (command=%s, type=%s, model=%s, effort=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model, agCfg.Effort)
	return ag
}

// createHTTPAgent 校验端点后创建 HTTP Agent。
func createHTTPAgent(name string, agCfg config.AgentConfig) agent.Agent {
	if agCfg.Endpoint == "" {
		log.Printf("[agent] HTTP agent %q has no endpoint", name)
		return nil
	}
	ag, err := agent.NewHTTPAgent(agent.HTTPAgentConfig{
		Endpoint: agCfg.Endpoint, APIKey: agCfg.APIKey, Headers: agCfg.Headers,
		Model: agCfg.Model, SystemPrompt: agCfg.SystemPrompt, MaxHistory: agCfg.MaxHistory,
	})
	if err != nil {
		log.Printf("[agent] invalid HTTP agent %q config: %v", name, err)
		return nil
	}
	log.Printf("[agent] created HTTP agent: %s (endpoint=%s, model=%s)", name, agCfg.Endpoint, agCfg.Model)
	return ag
}

// createCompanionAgent 创建并启动持久 Companion Agent。
func createCompanionAgent(ctx context.Context, name string, agCfg config.AgentConfig) agent.Agent {
	if agCfg.Command == "" {
		log.Printf("[agent] companion agent %q has no command", name)
		return nil
	}
	ag := agent.NewCompanionAgent(agent.CompanionAgentConfig{
		Name: name, Command: agCfg.Command, Args: agCfg.Args, Cwd: agCfg.Cwd,
		Env: agCfg.Env, Model: agCfg.Model, AutoLaunch: companionAutoLaunchEnabled(name, agCfg),
	})
	if err := ag.Start(ctx); err != nil {
		log.Printf("[agent] failed to start companion agent %q: %v", name, err)
		return nil
	}
	log.Printf("[agent] started companion agent: %s (command=%s, type=%s)", name, agCfg.Command, agCfg.Type)
	return ag
}

// startACPAgentWithRetry 为 Codex 状态库初始化错误提供有限次数重试。
func startACPAgentWithRetry(ctx context.Context, name string, agCfg config.AgentConfig, protocolTrace ...observability.ProtocolRecorder) (*agent.ACPAgent, error) {
	if err := agCfg.ValidateCodexPermissionConfig(); err != nil {
		return nil, err
	}
	attempts := 1
	if isCodexAppServerAgent(agCfg) {
		attempts = 3
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ag := newACPAgentFromConfig(name, agCfg, protocolTrace...)
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

// newACPAgentFromConfig 将持久化配置转换为 ACP 运行时配置。
func newACPAgentFromConfig(name string, agCfg config.AgentConfig, protocolTrace ...observability.ProtocolRecorder) *agent.ACPAgent {
	return agent.NewACPAgent(acpAgentConfigFromConfig(name, agCfg, protocolTrace...))
}

func acpAgentConfigFromConfig(name string, agCfg config.AgentConfig, protocolTrace ...observability.ProtocolRecorder) agent.ACPAgentConfig {
	result := agent.ACPAgentConfig{
		ConfiguredName:   name,
		Command:          agCfg.Command,
		LocalCommand:     agCfg.LocalCommand,
		Args:             agCfg.Args,
		Cwd:              agCfg.Cwd,
		Env:              agCfg.Env,
		Model:            agCfg.Model,
		Effort:           agCfg.Effort,
		ApprovalPolicy:   agCfg.EffectiveApprovalPolicy(),
		ApprovalReviewer: agCfg.EffectiveApprovalReviewer(),
		SandboxMode:      agCfg.EffectiveSandboxMode(),
		SystemPrompt:     agCfg.SystemPrompt,
		AppServerSocket:  agCfg.AppServerSocket,
		RunAsUser:        agCfg.RunAsUser,
		RunAsEnv:         agCfg.RunAsEnv,
	}
	if len(protocolTrace) > 0 {
		result.ProtocolTrace = protocolTrace[0]
	}
	return result
}

// isCodexAppServerAgent 判断配置是否启动 Codex app-server 协议。
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

// isRetryableCodexStateRuntimeError 识别可通过重新启动恢复的 Codex 状态库错误。
func isRetryableCodexStateRuntimeError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "failed to initialize sqlite state runtime") ||
		strings.Contains(text, "failed to initialize state runtime")
}

// companionAutoLaunchEnabled 读取 Companion 的显式自动启动开关。
func companionAutoLaunchEnabled(_ string, agCfg config.AgentConfig) bool {
	if agCfg.AutoLaunch != nil {
		return *agCfg.AutoLaunch
	}
	return false
}
