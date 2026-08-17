package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
)

var (
	codexACPStartupRetryDelay = 2 * time.Second
	startACPAgentCall         = func(ctx context.Context, ag *agent.ACPAgent) error { return ag.Start(ctx) }
)

// createAgentByName 按配置名称创建 Agent；配置缺失或启动失败时返回 nil。
func createAgentByName(ctx context.Context, cfg *config.Config, name string, protocolTrace ...observability.ProtocolRecorder) agent.Agent {
	ag, err := createAgentByNameWithError(ctx, cfg, name, protocolTrace...)
	if err != nil {
		log.Printf("[agent] %v", err)
		return nil
	}
	return ag
}

// createAgentByNameWithError 保留 Agent 启动的原始错误，供消息侧返回可操作的
// 恢复提示；createAgentByName 继续保持历史的 nil 返回契约。
func createAgentByNameWithError(ctx context.Context, cfg *config.Config, name string, protocolTrace ...observability.ProtocolRecorder) (agent.Agent, error) {
	agCfg, ok := cfg.Agents[name]
	if !ok {
		return nil, fmt.Errorf("agent %q not found in config", name)
	}
	if name == "claude" && agCfg.Type != "acp" {
		return nil, fmt.Errorf("Claude remote backend only supports ACP; run weclaw config agent")
	}
	if name == "codex" && agCfg.Type == "cli" {
		return nil, fmt.Errorf("legacy codex exec backend is disabled; migrate to the shared app-server runtime")
	}

	switch agCfg.Type {
	case "acp":
		ag, err := startACPAgentWithRetry(ctx, name, agCfg, protocolTrace...)
		if err != nil {
			return nil, fmt.Errorf("failed to start ACP agent %q: %w", name, err)
		}
		log.Printf("[agent] started ACP agent: %s (command=%s, type=%s, model=%s, effort=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model, agCfg.Effort)
		return ag, nil
	case "cli":
		return createCLIAgent(name, agCfg), nil
	case "http":
		ag := createHTTPAgent(name, agCfg)
		if ag == nil {
			return nil, fmt.Errorf("HTTP agent %q is invalid or incomplete", name)
		}
		return ag, nil
	case "companion":
		ag := createCompanionAgent(ctx, name, agCfg)
		if ag == nil {
			return nil, fmt.Errorf("companion agent %q failed to start", name)
		}
		return ag, nil
	default:
		return nil, fmt.Errorf("unknown agent type %q for %q", agCfg.Type, name)
	}
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
	ag := newACPAgentFromConfig(name, agCfg, protocolTrace...)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := startACPAgentCall(ctx, ag); err != nil {
			lastErr = err
			if attempt == attempts || !isRetryableCodexStateRuntimeError(err) ||
				errors.Is(err, agent.ErrCodexCLIAutoUpdateFailed) {
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
	codexAppDaemon := agCfg.CodexAppDaemon
	if agCfg.EffectiveCodexMultiFrontend() {
		enabled := true
		codexAppDaemon = &enabled
	}
	result := agent.ACPAgentConfig{
		ConfiguredName:     name,
		Command:            agCfg.Command,
		LocalCommand:       agCfg.LocalCommand,
		Args:               agCfg.Args,
		Cwd:                agCfg.Cwd,
		Env:                agCfg.Env,
		Model:              agCfg.Model,
		Effort:             agCfg.Effort,
		ApprovalPolicy:     agCfg.EffectiveApprovalPolicy(),
		ApprovalReviewer:   agCfg.EffectiveApprovalReviewer(),
		SandboxMode:        agCfg.EffectiveSandboxMode(),
		SystemPrompt:       agCfg.SystemPrompt,
		AppServerSocket:    agCfg.AppServerSocket,
		CodexHostMode:      agCfg.EffectiveCodexHostMode(),
		CodexAutoUpdate:    agCfg.EffectiveCodexAutoUpdate(),
		CodexAppDaemon:     codexAppDaemon,
		CodexDesktopBridge: codexDesktopBridgeEnabled(agCfg),
		RunAsUser:          agCfg.RunAsUser,
		RunAsEnv:           agCfg.RunAsEnv,
	}
	if len(protocolTrace) > 0 {
		result.ProtocolTrace = protocolTrace[0]
	}
	return result
}

// codexDesktopBridgeEnabled 为原生 Codex 的单用户 macOS 拓扑启用 App IPC 协调。
// auto 可以在没有 daemon 时选择 App Host；显式 daemon 只用 IPC 做
// frontend 状态探测和 thread 回交，不改变用户选定的 Host。
func codexDesktopBridgeEnabled(agCfg config.AgentConfig) bool {
	hostMode := agCfg.EffectiveCodexHostMode()
	return runtime.GOOS == "darwin" &&
		isCodexAppServerAgent(agCfg) &&
		(hostMode == "auto" || hostMode == "daemon") &&
		strings.TrimSpace(agCfg.AppServerSocket) == "" &&
		strings.TrimSpace(agCfg.RunAsUser) == ""
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
	return agent.IsCodexStateRuntimeError(err)
}

// companionAutoLaunchEnabled 读取 Companion 的显式自动启动开关。
func companionAutoLaunchEnabled(_ string, agCfg config.AgentConfig) bool {
	if agCfg.AutoLaunch != nil {
		return *agCfg.AutoLaunch
	}
	return false
}
