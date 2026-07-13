package agent

import (
	"path/filepath"
	"strings"
)

func detectACPProtocol(command string, args []string) string {
	base := strings.ToLower(filepath.Base(command))
	// codex-acp is a standard ACP wrapper, NOT codex app-server
	// Only `codex app-server` uses the codex-native protocol
	if base == "codex" || base == "codex.exe" {
		for _, arg := range args {
			if arg == "app-server" {
				return protocolCodexAppServer
			}
		}
	}
	return protocolLegacyACP
}

type acpAgentOptions struct {
	desktopProbe codexDesktopOwnerProbe
	protocol     string
	stateFile    string
}

// NewACPAgent creates a new ACP agent.
func NewACPAgent(cfg ACPAgentConfig) *ACPAgent {
	return newACPAgent(cfg, acpAgentOptions{})
}

// newACPAgent 允许包内测试注入 Desktop probe，不改变公开构造签名。
func newACPAgent(cfg ACPAgentConfig, options acpAgentOptions) *ACPAgent {
	if cfg.Command == "" {
		cfg.Command = "claude-agent-acp"
	}
	if cfg.Cwd == "" {
		cfg.Cwd = defaultWorkspace()
	}
	protocol := detectACPProtocol(cfg.Command, cfg.Args)
	stateFile := cfg.StateFile
	if stateFile == "" {
		stateFile = defaultACPStateFile(cfg.Command, cfg.Args, cfg.Cwd, protocol)
	}
	options.protocol = protocol
	options.stateFile = stateFile
	a := buildACPAgent(cfg, options)
	a.configureCodexRuntime(options.desktopProbe)
	a.loadState()
	return a
}

// buildACPAgent 初始化不依赖外部运行时的进程内状态。
func buildACPAgent(cfg ACPAgentConfig, options acpAgentOptions) *ACPAgent {
	a := &ACPAgent{
		configuredName:           strings.TrimSpace(cfg.ConfiguredName),
		command:                  cfg.Command,
		localCommand:             strings.TrimSpace(cfg.LocalCommand),
		args:                     cfg.Args,
		model:                    cfg.Model,
		effort:                   cfg.Effort,
		approvalPolicy:           strings.TrimSpace(cfg.ApprovalPolicy),
		approvalReviewer:         strings.TrimSpace(cfg.ApprovalReviewer),
		sandboxMode:              strings.TrimSpace(cfg.SandboxMode),
		systemPrompt:             cfg.SystemPrompt,
		cwd:                      cfg.Cwd,
		env:                      cfg.Env,
		runAs:                    runAsUserSpec{User: cfg.RunAsUser, PreserveEnv: cfg.RunAsEnv},
		protocol:                 options.protocol,
		sessions:                 make(map[string]string),
		pendingPersistedSessions: make(map[string]string),
		sessionGenerations:       make(map[string]uint64),
		bindingRevisions:         make(map[string]uint64),
		threads:                  make(map[string]string),
		resumeOnFirstUse:         make(map[string]bool),
		conversationCwds:         make(map[string]string),
		stateFile:                options.stateFile,
		claudeSessionConfigs:     make(map[string][]acpSessionConfigOption),
		claudeConfigRevisions:    make(map[string]uint64),
		pending:                  make(map[int64]chan *rpcResponse),
		notifyCh:                 make(map[string]chan *sessionUpdate),
		turnCh:                   make(map[string]chan *codexTurnEvent),
		desktopProbe:             options.desktopProbe,
	}
	return a
}

// configureCodexRuntime 装配仅供 app-server 协议使用的 Desktop 所有权状态。
func (a *ACPAgent) configureCodexRuntime(probe codexDesktopOwnerProbe) {
	if a.protocol != protocolCodexAppServer {
		return
	}
	if probe == nil {
		a.desktopRuntime = newCodexDesktopRuntime()
		probe = a.desktopRuntime
	}
	a.desktopProbe = probe
	a.codexOwners = newCodexRuntimeOwnerRegistry(probe)
	if a.desktopRuntime == nil {
		return
	}
	a.desktopRuntime.setOwnerRegistry(a.codexOwners)
	a.desktopRuntime.setEventHandler(func(threadID string, events []*codexTurnEvent) {
		for _, event := range events {
			a.dispatchToTurnCh(threadID, event)
		}
	})
}
