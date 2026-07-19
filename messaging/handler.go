package messaging

import (
	"context"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

// AgentFactory creates an agent by config name. Returns nil if the name is unknown.
type AgentFactory func(ctx context.Context, name string) agent.Agent

// SaveDefaultFunc persists the default agent name to config file.
type SaveDefaultFunc func(name string) error

// CDNDownloader 用于下载微信 CDN 中的入站文件，便于测试注入。
type CDNDownloader func(ctx context.Context, encryptQueryParam string, aesKey string) ([]byte, error)

// ProgressChatAgent 支持在聊天过程中输出增量内容。
type ProgressChatAgent interface {
	ChatWithProgress(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error)
}

// AgentMeta holds static config info about an agent (for /status display).
type AgentMeta struct {
	Name    string
	Type    string // "acp", "cli", "http", "companion"
	Command string // binary path or endpoint
	Model   string
	Effort  string
}

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	mu                      sync.RWMutex
	defaultName             string
	agents                  map[string]agent.Agent // name -> running agent
	agentStarts             map[string]*agentStartState
	agentMetas              []AgentMeta       // all configured agents (for /status)
	agentWorkDirs           map[string]string // agent name -> configured/runtime cwd
	configuredAgentWorkDirs map[string]string // agent name -> 启动配置 cwd，不随会话切换变化
	customAliases           map[string]string // custom alias -> agent name (from config)
	factory                 AgentFactory
	saveDefault             SaveDefaultFunc
	saveDir                 string   // directory to save images/files to
	allowedWorkspaceRoots   []string // /cwd 允许切换的根目录；空=禁止远程切换
	adminUsers              map[string]struct{}
	rateLimiter             *userRateLimiter
	rateLimitPerMinute      int
	audit                   auditLogger
	startedAt               time.Time
	agentInvocations        atomic.Int64
	agentErrors             atomic.Int64
	lastDedupCleanup        atomic.Int64
	seenMsgs                sync.Map // map[int64]time.Time — dedup by message_id
	cdnDownloader           CDNDownloader
	progressConfig          config.ProgressConfig
	agentProgressConfigs    map[string]config.ProgressConfig
	platformProgressConfigs map[string]config.ProgressConfig
	platformDefaultAgents   map[string]string
	agentSessions           *agentSessionStore
	seenTextMsgs            sync.Map // map[string]time.Time — MessageID 为 0 时按文本去重
	codexSessions           *codexSessionStore
	feishuIdentities        *feishuIdentityStore
	taskLocksMu             sync.Mutex
	taskLocks               map[string]*executionLock
	activeTasksMu           sync.Mutex
	activeTasks             map[string]*activeAgentTask
	pendingApprovalsMu      sync.Mutex
	pendingApprovals        map[string]*pendingApproval
	yoloUsers               sync.Map // userID -> struct{}：开启自动放行(yolo)的用户
	codexLocalSessionDir    string
	claudeSessions          *claudeSessionStore
	codexBrowseMu           sync.Mutex
	codexBrowseWorkspaces   map[string]string
	feishuWorkspaceChoices  feishuWorkspaceChoiceStore
	feishuNavSnapshots      feishuNavigationSnapshotStore
	serviceAdminMu          sync.Mutex
	serviceAdminExecutor    ServiceAdminCommandExecutor
	adminTimeout            time.Duration
	codexCommandTimeout     time.Duration
	codexLockWaitTimeout    time.Duration
}

const (
	pendingCodexPreviewRunes = 120
	pendingApprovalTimeout   = 5 * time.Minute
	feishuSessionMetadataKey = "feishu_session_key"
)

var ansiEscapePattern = regexp.MustCompile(`\x1B\[[0-?]*[ -/]*[@-~]`)

// HandleMessage processes a single platform-agnostic incoming message.
func (h *Handler) HandleMessage(ctx context.Context, incoming platform.IncomingMessage, reply platform.Replier) {
	if reply == nil {
		log.Printf("[handler] received message from %s without replier", incoming.UserID)
		return
	}
	h.handlePlatformMessage(ctx, incoming, reply)
}

// HandlePlatformMessage 保留给旧测试和外部调用点，内部统一转到 HandleMessage。
func (h *Handler) HandlePlatformMessage(ctx context.Context, incoming platform.IncomingMessage, reply platform.Replier) {
	h.HandleMessage(ctx, incoming, reply)
}

func (h *Handler) handlePlatformMessage(ctx context.Context, msg platform.IncomingMessage, replyWriter platform.Replier) {
	if h.isDuplicatePlatformMessage(msg) {
		return
	}
	runtime := platformMessageRuntime{
		ctx: contextWithWorkspaceAdmin(ctx, h.isAdminMessage(msg)),
		msg: msg, reply: replyWriter, routeUserID: platformMessageRouteUserID(msg),
		text: strings.TrimSpace(platformMessageText(msg)),
	}
	if h.handlePlatformRawCommand(runtime) {
		return
	}
	runtime, ready := h.preparePlatformMessage(runtime)
	if !ready {
		return
	}
	h.dispatchPlatformMessage(runtime)
}
