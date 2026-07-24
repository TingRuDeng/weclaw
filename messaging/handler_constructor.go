package messaging

import (
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

// NewHandler creates a new message handler.
func NewHandler(factory AgentFactory, saveDefault SaveDefaultFunc) *Handler {
	return &Handler{
		agents:                  make(map[string]agent.Agent),
		agentStarts:             make(map[string]*agentStartState),
		agentWorkDirs:           make(map[string]string),
		factory:                 factory,
		saveDefault:             saveDefault,
		progressConfig:          config.DefaultProgressConfig(),
		agentProgressConfigs:    make(map[string]config.ProgressConfig),
		platformProgressConfigs: make(map[string]config.ProgressConfig),
		platformDefaultAgents:   make(map[string]string),
		sessions:                newSessionService(),
		feishuIdentities:        newFeishuIdentityStore(),
		taskLocks:               make(map[string]*executionLock),
		pendingApprovals:        make(map[string]*pendingApproval),
		resolvedApprovalCodes:   make(map[string]time.Time),
		codexLocalSessionDir:    defaultCodexLocalSessionDir(),
		codexBrowseWorkspaces:   make(map[string]string),
		codexTaskCardFocus:      make(map[string]string),
		serviceAdminExecutor:    defaultServiceAdminCommandExecutor,
		adminTimeout:            adminCommandTimeout,
		codexCommandTimeout:     defaultCodexSessionCommandTimeout,
		codexLockWaitTimeout:    defaultCodexSessionLockWaitTimeout,
		startedAt:               time.Now(),
	}
}

// SetCDNDownloader 注入平台入站附件下载能力；应在开始接收消息前调用。
func (h *Handler) SetCDNDownloader(downloader CDNDownloader) {
	h.cdnDownloader = downloader
}
