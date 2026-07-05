package messaging

import (
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/wechat"
)

// NewHandler creates a new message handler.
func NewHandler(factory AgentFactory, saveDefault SaveDefaultFunc) *Handler {
	return &Handler{
		agents:                  make(map[string]agent.Agent),
		agentWorkDirs:           make(map[string]string),
		factory:                 factory,
		saveDefault:             saveDefault,
		cdnDownloader:           wechat.DownloadFileFromCDN,
		progressConfig:          config.DefaultProgressConfig(),
		agentProgressConfigs:    make(map[string]config.ProgressConfig),
		platformProgressConfigs: make(map[string]config.ProgressConfig),
		platformDefaultAgents:   make(map[string]string),
		codexSessions:           newCodexSessionStore(),
		taskLocks:               make(map[string]*sync.Mutex),
		activeTasks:             make(map[string]*activeAgentTask),
		pendingCodexRuns:        make(map[string]string),
		pendingApprovals:        make(map[string]*pendingApproval),
		codexLocalSessionDir:    defaultCodexLocalSessionDir(),
		claudeSessions:          newClaudeSessionStore(),
		claudeLocalSessionDir:   defaultClaudeLocalSessionDir(),
		codexBrowseWorkspaces:   make(map[string]string),
		codexLocalEntries:       make(map[string]codexLocalEntryState),
		codexAppOpener:          defaultCodexAppOpener,
		codexCLIResumeOpener:    defaultCodexCLIResumeOpener,
		claudeCLIResumeOpener:   defaultClaudeCLIResumeOpener,
		serviceAdminExecutor:    defaultServiceAdminCommandExecutor,
		startedAt:               time.Now(),
	}
}
