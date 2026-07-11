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
	a := &ACPAgent{
		command:                     cfg.Command,
		args:                        cfg.Args,
		model:                       cfg.Model,
		effort:                      cfg.Effort,
		approvalPolicy:              strings.TrimSpace(cfg.ApprovalPolicy),
		approvalReviewer:            strings.TrimSpace(cfg.ApprovalReviewer),
		sandboxMode:                 strings.TrimSpace(cfg.SandboxMode),
		systemPrompt:                cfg.SystemPrompt,
		cwd:                         cfg.Cwd,
		env:                         cfg.Env,
		runAs:                       runAsUserSpec{User: cfg.RunAsUser, PreserveEnv: cfg.RunAsEnv},
		protocol:                    protocol,
		sessions:                    make(map[string]string),
		threads:                     make(map[string]string),
		resumeOnFirstUse:            make(map[string]bool),
		usageLimitRefreshOnNextTurn: make(map[string]bool),
		conversationCwds:            make(map[string]string),
		stateFile:                   stateFile,
		history:                     make(map[string][]acpHistoryMessage),
		pending:                     make(map[int64]chan *rpcResponse),
		notifyCh:                    make(map[string]chan *sessionUpdate),
		turnCh:                      make(map[string]chan *codexTurnEvent),
		desktopProbe:                options.desktopProbe,
	}
	if protocol == protocolCodexAppServer {
		probe := options.desktopProbe
		if probe == nil {
			a.desktopRuntime = newCodexDesktopRuntime()
			probe = a.desktopRuntime
		}
		a.desktopProbe = probe
		a.codexOwners = newCodexRuntimeOwnerRegistry(probe)
		if a.desktopRuntime != nil {
			a.desktopRuntime.setOwnerRegistry(a.codexOwners)
			a.desktopRuntime.setEventHandler(func(threadID string, events []*codexTurnEvent) {
				for _, event := range events {
					a.dispatchToTurnCh(threadID, event)
				}
			})
		}
	}
	a.loadState()
	return a
}
