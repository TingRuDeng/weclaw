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
	desktopProbe  codexDesktopOwnerProbe
	desktopBridge bool
	protocol      string
	stateFile     string
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
		stateFile = defaultACPStateFile(acpStateFileIdentity{
			command: cfg.Command, args: cfg.Args, cwd: cfg.Cwd, protocol: protocol,
		})
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
	configuredHostMode := normalizeAgentCodexHostMode(cfg.CodexHostMode)
	desktopCoordination := cfg.CodexDesktopBridge || options.desktopBridge
	// options.desktopBridge 是包内测试的完整 Desktop Host 注入点；生产
	// 配置中只有 auto 能从共享 Host 切换到 Desktop Host。保留
	// 历史直接构造器“省略 Host mode + 显式 bridge”的既有语义。
	desktopHostSelection := options.desktopBridge ||
		(cfg.CodexDesktopBridge &&
			(configuredHostMode == codexHostModeAuto || strings.TrimSpace(cfg.CodexHostMode) == ""))
	a := &ACPAgent{
		configuredName:             strings.TrimSpace(cfg.ConfiguredName),
		command:                    cfg.Command,
		localCommand:               strings.TrimSpace(cfg.LocalCommand),
		args:                       cfg.Args,
		model:                      cfg.Model,
		effort:                     cfg.Effort,
		approvalPolicy:             strings.TrimSpace(cfg.ApprovalPolicy),
		approvalReviewer:           strings.TrimSpace(cfg.ApprovalReviewer),
		sandboxMode:                strings.TrimSpace(cfg.SandboxMode),
		systemPrompt:               cfg.SystemPrompt,
		cwd:                        cfg.Cwd,
		env:                        cfg.Env,
		runAs:                      runAsUserSpec{User: cfg.RunAsUser, PreserveEnv: cfg.RunAsEnv},
		protocol:                   options.protocol,
		sessions:                   make(map[string]string),
		pendingPersistedSessions:   make(map[string]string),
		sessionGenerations:         make(map[string]uint64),
		bindingRevisions:           make(map[string]uint64),
		threads:                    make(map[string]string),
		codexThreadConfigs:         make(map[string]CodexThreadConfig),
		codexThreadConfigRevisions: make(map[string]uint64),
		codexThreadProviders:       make(map[string]string),
		resumeOnFirstUse:           make(map[string]bool),
		conversationCwds:           make(map[string]string),
		stateFile:                  options.stateFile,
		codexHostSocket:            strings.TrimSpace(cfg.AppServerSocket),
		codexHostMode:              configuredHostMode,
		codexAutoUpdate:            strings.ToLower(strings.TrimSpace(cfg.CodexAutoUpdate)),
		codexAppReuseDaemon:        cloneBoolPointer(cfg.CodexAppDaemon),
		claudeSessionConfigs:       make(map[string][]acpSessionConfigOption),
		claudeConfigRevisions:      make(map[string]uint64),
		claudeLoadedSessions:       make(map[string]claudeLoadedSessionState),
		claudeSessionCommands:      make(map[string]claudeSessionCommandState),
		claudeSessionTitles:        make(map[string]claudeSessionTitleState),
		claudeCommandChanged:       make(chan struct{}),
		notifyCh:                   make(map[string]chan *sessionUpdate),
		turnCh:                     make(map[string]chan *codexTurnEvent),
		turnObservers:              make(map[string]map[uint64]*codexTurnObserverMailbox),
		pendingTurnInteractions:    make(map[string]map[string]*codexTurnEvent),
		desktopProbe:               options.desktopProbe,
		codexDesktopCoordination:   desktopCoordination,
		codexDesktopHostSelection:  desktopHostSelection,
		appServerGate:              newCodexAppServerGate(),
		protocolTrace:              cfg.ProtocolTrace,
	}
	a.codexHostMode = a.resolveAgentCodexHostMode()
	return a
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// configureCodexRuntime 为原生 app-server 装配 thread 绑定与 writer lease。
// macOS auto/daemon 拓扑都可装配 Desktop IPC 协调；是否允许 App
// 成为 Host 由 codexDesktopHostSelection 独立决定。
func (a *ACPAgent) configureCodexRuntime(probe codexDesktopOwnerProbe) {
	if a.protocol != protocolCodexAppServer {
		return
	}
	if probe == nil && a.codexDesktopCoordination {
		probe = newSystemCodexDesktopRuntime()
	}
	a.codexOwners = newCodexRuntimeOwnerRegistry(probe)
	if probe == nil {
		return
	}
	// 生产 bridge 仍沿用“多 frontend binding + 单 thread lease”，不恢复
	// 已退役的 route 独占 owner；测试注入默认保留旧控制语义。
	if a.codexDesktopCoordination {
		a.codexOwners.enforceControl = false
	}
	a.desktopProbe = probe
	if runtime, ok := probe.(*codexDesktopRuntime); ok {
		a.desktopRuntime = runtime
	}
	if a.desktopRuntime == nil {
		return
	}
	a.desktopRuntime.setOwnerRegistry(a.codexOwners)
	a.desktopRuntime.setAuthoritative(func() bool {
		return a.codexRuntimeModeSnapshot() == CodexRuntimeDesktop
	})
	a.desktopRuntime.setDisconnectHandler(a.handleCodexDesktopDisconnect)
	a.desktopRuntime.setEventHandler(func(threadID string, events []*codexTurnEvent) {
		for _, event := range events {
			a.dispatchDesktopTurnEvent(threadID, event)
		}
	})
}
