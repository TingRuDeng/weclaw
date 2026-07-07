package messaging

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

// AgentFactory creates an agent by config name. Returns nil if the name is unknown.
type AgentFactory func(ctx context.Context, name string) agent.Agent

// SaveDefaultFunc persists the default agent name to config file.
type SaveDefaultFunc func(name string) error

// CDNDownloader 用于下载微信 CDN 中的入站文件，便于测试注入。
type CDNDownloader func(ctx context.Context, encryptQueryParam string, aesKey string) ([]byte, error)

// CodexAppOpener 用于打开当前工作区的 Codex App，便于测试替换外部进程。
type CodexAppOpener func(ctx context.Context, command string, workspaceRoot string) error

// CodexCLIResumeOpener 用于把当前 Codex thread 打开到本地 CLI/TUI。
type CodexCLIResumeOpener func(ctx context.Context, command string, workspaceRoot string, threadID string) error

// ClaudeCLIResumeOpener 用于把当前 Claude session 打开到本地 CLI。
type ClaudeCLIResumeOpener func(ctx context.Context, command string, workspaceRoot string, sessionID string) error

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
	customAliases           map[string]string // custom alias -> agent name (from config)
	factory                 AgentFactory
	saveDefault             SaveDefaultFunc
	contextTokens           sync.Map // map[userID]contextToken
	saveDir                 string   // directory to save images/files to
	allowedWorkspaceRoots   []string // /cwd 允许切换的根目录；空=不限制
	adminUsers              map[string]struct{}
	rateLimiter             *userRateLimiter
	rateLimitPerMinute      int
	audit                   auditLogger
	startedAt               time.Time
	agentInvocations        atomic.Int64
	agentErrors             atomic.Int64
	seenMsgs                sync.Map // map[int64]time.Time — dedup by message_id
	cdnDownloader           CDNDownloader
	progressConfig          config.ProgressConfig
	agentProgressConfigs    map[string]config.ProgressConfig
	platformProgressConfigs map[string]config.ProgressConfig
	platformDefaultAgents   map[string]string
	seenTextMsgs            sync.Map // map[string]time.Time — MessageID 为 0 时按文本去重
	codexSessions           *codexSessionStore
	taskLocksMu             sync.Mutex
	taskLocks               map[string]*sync.Mutex
	activeTasksMu           sync.Mutex
	activeTasks             map[string]*activeAgentTask
	pendingCodexConfirmsMu  sync.Mutex
	pendingCodexConfirms    map[string]string
	pendingApprovalsMu      sync.Mutex
	pendingApprovals        map[string]*pendingApproval
	yoloUsers               sync.Map // userID -> struct{}：开启自动放行(yolo)的用户
	codexLocalSessionDir    string
	claudeSessions          *claudeSessionStore
	claudeLocalSessionDir   string
	codexBrowseMu           sync.Mutex
	codexBrowseWorkspaces   map[string]string
	codexLocalEntries       map[string]codexLocalEntryState
	codexAppOpener          CodexAppOpener
	codexCLIResumeOpener    CodexCLIResumeOpener
	claudeCLIResumeOpener   ClaudeCLIResumeOpener
	serviceAdminMu          sync.Mutex
	serviceAdminExecutor    ServiceAdminCommandExecutor
}

const (
	pendingCodexPreviewRunes = 120
	pendingApprovalTimeout   = 5 * time.Minute
	feishuSessionMetadataKey = "feishu_session_key"
)

var ansiEscapePattern = regexp.MustCompile(`\x1B\[[0-?]*[ -/]*[@-~]`)

// codexAgentTaskOptions 保存 Codex 后台任务需要的上下文，避免长参数列表掩盖调用意图。
type codexAgentTaskOptions struct {
	ctx         context.Context
	userID      string
	routeUserID string
	reply       platform.Replier
	agentName   string
	message     string
	clientID    string
	replyPrefix string
	agent       agent.Agent
	progressCfg config.ProgressConfig
}

// codexAgentTaskRuntime 保存已经登记 active task 后的运行时资源。
type codexAgentTaskRuntime struct {
	opts              codexAgentTaskOptions
	agentCtx          context.Context
	cancelTaskTimeout context.CancelFunc
	executionKey      string
	route             codexConversationRoute
	task              *activeAgentTask
}

type codexConversationRoute struct {
	bindingKey     string
	workspaceRoot  string
	conversationID string
}

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
	if msg.MessageID != "" && msg.MessageID != "0" {
		if _, loaded := h.seenMsgs.LoadOrStore(platformMessageDedupKey(msg), time.Now()); loaded {
			return
		}
		go h.cleanSeenMsgs(h.duplicateTTL())
	}
	routeUserID := platformMessageRouteUserID(msg)

	text := strings.TrimSpace(platformMessageText(msg))
	if msg.RawCommand != nil && msg.RawCommand.Action == "stop" {
		sendPlatformText(ctx, replyWriter, msg.UserID, h.handleStopActiveTask(ctx, msg.UserID, routeUserID))
		return
	}
	if msg.RawCommand != nil && msg.RawCommand.Action == "choice" {
		if h.consumePendingApprovalForKey(msg.UserID, msg.RawCommand.Value["approval_key"], msg.RawCommand.Value["choice"]) {
			reportCardActionResult(msg.RawCommand, platform.CardActionResultConsumed)
			return
		}
		if isApprovalChoiceCommand(msg.RawCommand) {
			if !reportCardActionResult(msg.RawCommand, platform.CardActionResultExpired) {
				sendPlatformText(ctx, replyWriter, msg.UserID, staleApprovalReply())
			}
			return
		}
	}
	if h.consumePendingApproval(msg.UserID, text) {
		return
	}
	if file, ok := firstAttachment(msg.Attachments, platform.AttachmentFile); ok {
		fileText, handled := h.handleFileAttachment(ctx, msg.UserID, replyWriter, file, text)
		if !handled {
			return
		}
		text = strings.TrimSpace(fileText)
	}
	if img, ok := firstAttachment(msg.Attachments, platform.AttachmentImage); ok && text != "" {
		imageText, handled := h.handleImageAttachment(ctx, msg.UserID, replyWriter, img, text)
		if !handled {
			return
		}
		text = strings.TrimSpace(imageText)
	}
	if text == "" {
		if img, ok := firstAttachment(msg.Attachments, platform.AttachmentImage); ok && h.saveDir != "" {
			h.handleImageAttachmentSave(ctx, msg.UserID, replyWriter, img)
			return
		}
		log.Printf("[handler] received non-text message from %s, skipping", msg.UserID)
		return
	}
	if (msg.MessageID == "" || msg.MessageID == "0") && h.isDuplicateTextMessage(msg.UserID, msg.ContextToken, text) {
		sendPlatformText(ctx, replyWriter, msg.UserID, duplicateTaskReply())
		return
	}

	log.Printf("[handler] received from %s: %q", msg.UserID, truncate(text, 80))
	h.contextTokens.Store(msg.UserID, msg.ContextToken)

	clientID := NewClientID()
	if wxReply, ok := replyWriter.(*wechat.Replier); ok {
		wxReply.ClientID = clientID
	}
	sendText := func(text string) {
		sendPlatformText(ctx, replyWriter, msg.UserID, text)
	}

	trimmed := strings.TrimSpace(text)
	if h.saveDir != "" && IsURL(trimmed) {
		rawURL := ExtractURL(trimmed)
		if rawURL != "" {
			log.Printf("[handler] saving URL to linkhoard: %s", rawURL)
			title, err := SaveLinkToLinkhoard(ctx, h.saveDir, rawURL)
			if err != nil {
				log.Printf("[handler] link save failed: %v", err)
				sendText(fmt.Sprintf("保存失败: %v", err))
				return
			}
			sendText(fmt.Sprintf("已保存: %s", title))
			return
		}
	}

	if h.handleBuiltInPlatformCommand(ctx, platformCommandRequest{
		Message:     msg,
		RouteUserID: routeUserID,
		Reply:       replyWriter,
		Trimmed:     trimmed,
		ClientID:    clientID,
	}) {
		return
	}

	if h.handlePendingCodexConfirmation(ctx, msg.Platform, msg.AccountID, msg.UserID, routeUserID, trimmed, replyWriter, clientID) {
		return
	}

	if !h.allowAgentInvocation(routeUserID) {
		log.Printf("[handler] rate limit exceeded for %s", routeUserID)
		sendText("请求过于频繁，请稍后再试。")
		return
	}
	h.auditRecord(auditEntry{
		Platform: string(msg.Platform),
		User:     msg.UserID,
		Action:   "agent_message",
		Summary:  text,
	})

	agentNames, message := h.parseCommand(text)
	if len(agentNames) == 0 {
		h.sendToDefaultAgentForAccount(ctx, msg.Platform, msg.AccountID, msg.UserID, routeUserID, replyWriter, text, clientID)
		return
	}
	if message == "" {
		if len(agentNames) == 1 && h.isKnownAgent(agentNames[0]) {
			sendText(h.switchDefault(ctx, agentNames[0]))
		} else if len(agentNames) == 1 && !h.isKnownAgent(agentNames[0]) {
			h.sendToDefaultAgentForAccount(ctx, msg.Platform, msg.AccountID, msg.UserID, routeUserID, replyWriter, text, clientID)
		} else {
			sendText("Usage: specify one agent to switch, or add a message to broadcast")
		}
		return
	}

	knownNames := make([]string, 0, len(agentNames))
	for _, name := range agentNames {
		if h.isKnownAgent(name) {
			knownNames = append(knownNames, name)
		}
	}
	if len(knownNames) == 0 {
		h.sendToDefaultAgentForAccount(ctx, msg.Platform, msg.AccountID, msg.UserID, routeUserID, replyWriter, text, clientID)
		return
	}
	if len(knownNames) == 1 {
		h.sendToNamedAgentForAccount(ctx, msg.Platform, msg.AccountID, msg.UserID, routeUserID, replyWriter, knownNames[0], message, clientID)
		return
	}
	h.broadcastToAgents(broadcastAgentsRequest{
		ctx:          ctx,
		platformName: msg.Platform,
		accountID:    msg.AccountID,
		userID:       msg.UserID,
		routeUserID:  routeUserID,
		replyWriter:  replyWriter,
		names:        knownNames,
		message:      message,
	})
}
