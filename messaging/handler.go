package messaging

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
	"github.com/google/uuid"
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
	agentMetas              []AgentMeta            // all configured agents (for /status)
	agentWorkDirs           map[string]string      // agent name -> configured/runtime cwd
	customAliases           map[string]string      // custom alias -> agent name (from config)
	factory                 AgentFactory
	saveDefault             SaveDefaultFunc
	contextTokens           sync.Map // map[userID]contextToken
	saveDir                 string   // directory to save images/files to
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
	pendingCodexRunsMu      sync.Mutex
	pendingCodexRuns        map[string]string
	pendingApprovalsMu      sync.Mutex
	pendingApprovals        map[string]*pendingApproval
	codexLocalSessionDir    string
	claudeSessions          *claudeSessionStore
	claudeLocalSessionDir   string
	codexBrowseMu           sync.Mutex
	codexBrowseWorkspaces   map[string]string
	codexLocalEntries       map[string]codexLocalEntryState
	codexAppOpener          CodexAppOpener
	codexCLIResumeOpener    CodexCLIResumeOpener
	claudeCLIResumeOpener   ClaudeCLIResumeOpener
}

const (
	pendingCodexPreviewRunes = 120
	pendingApprovalTimeout   = 5 * time.Minute
)

var ansiEscapePattern = regexp.MustCompile(`\x1B\[[0-?]*[ -/]*[@-~]`)

type activeAgentTask struct {
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
	detached       bool
	pendingMessage string
}

type pendingApproval struct {
	choices chan string
	allowed map[string]bool
}

// codexAgentTaskOptions 保存 Codex 后台任务需要的上下文，避免长参数列表掩盖调用意图。
type codexAgentTaskOptions struct {
	ctx         context.Context
	userID      string
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
	}
}

// SetSaveDir sets the directory for saving images and files.
func (h *Handler) SetSaveDir(dir string) {
	h.saveDir = dir
}

// cleanSeenMsgs 清理超过 TTL 的消息去重缓存。
func (h *Handler) cleanSeenMsgs(ttl time.Duration) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	cutoff := time.Now().Add(-ttl)
	h.seenMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenMsgs.Delete(key)
		}
		return true
	})
	h.seenTextMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenTextMsgs.Delete(key)
		}
		return true
	})
}

func (h *Handler) duplicateTTL() time.Duration {
	cfg := h.resolveProgressConfig("")
	return durationSeconds(cfg.DuplicateTTLSeconds, 5*time.Minute)
}

func (h *Handler) isDuplicateTextMessage(userID string, contextToken string, text string) bool {
	key := buildTextDedupKey(userID, contextToken, text)
	if key == "" {
		return false
	}
	now := time.Now()
	if seenAt, loaded := h.seenTextMsgs.LoadOrStore(key, now); loaded {
		if t, ok := seenAt.(time.Time); ok && now.Sub(t) <= h.duplicateTTL() {
			return true
		}
		h.seenTextMsgs.Store(key, now)
	}
	go h.cleanSeenMsgs(h.duplicateTTL())
	return false
}

func buildTextDedupKey(userID string, contextToken string, text string) string {
	normalized := strings.Join(strings.Fields(text), " ")
	if userID == "" || normalized == "" {
		return ""
	}
	return userID + "\x00" + contextToken + "\x00" + normalized
}

func duplicateTaskReply() string {
	return "这条任务已经收到，正在处理中。\n\n完成后我会发送完整结果。"
}

// SetCustomAliases sets custom alias mappings from config.
func (h *Handler) SetCustomAliases(aliases map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.customAliases = aliases
}

// SetAgentMetas sets the list of all configured agents (for /status).
func (h *Handler) SetAgentMetas(metas []AgentMeta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentMetas = metas
}

// SetAgentWorkDirs sets the configured working directory for each agent.
func (h *Handler) SetAgentWorkDirs(workDirs map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.agentWorkDirs = make(map[string]string, len(workDirs))
	for name, dir := range workDirs {
		h.agentWorkDirs[name] = dir
	}
}

// SetProgressConfig 设置全局微信进度反馈配置。
func (h *Handler) SetProgressConfig(cfg config.ProgressConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cfg.Mode == "" {
		cfg = config.DefaultProgressConfig()
	}
	h.progressConfig = cfg
}

// SetAgentProgressConfigs 设置每个 Agent 的进度反馈覆盖配置。
func (h *Handler) SetAgentProgressConfigs(configs map[string]config.ProgressConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentProgressConfigs = make(map[string]config.ProgressConfig, len(configs))
	for name, cfg := range configs {
		h.agentProgressConfigs[name] = cfg
	}
}

// SetPlatformProgressConfigs 设置每个平台的进度反馈覆盖配置。
func (h *Handler) SetPlatformProgressConfigs(configs map[string]config.ProgressConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.platformProgressConfigs = make(map[string]config.ProgressConfig, len(configs))
	for name, cfg := range configs {
		h.platformProgressConfigs[name] = cfg
	}
}

// SetPlatformDefaultAgents 设置每个平台的默认 Agent 覆盖配置。
func (h *Handler) SetPlatformDefaultAgents(defaults map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.platformDefaultAgents = make(map[string]string, len(defaults))
	for name, agentName := range defaults {
		if trimmed := strings.TrimSpace(agentName); trimmed != "" {
			h.platformDefaultAgents[name] = trimmed
		}
	}
}

// SetCodexAppOpener 设置 Codex App 打开器，主要用于测试外部进程调用。
func (h *Handler) SetCodexAppOpener(opener CodexAppOpener) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.codexAppOpener = opener
}

// SetCodexCLIResumeOpener 设置 Codex CLI resume 打开器，主要用于测试外部进程调用。
func (h *Handler) SetCodexCLIResumeOpener(opener CodexCLIResumeOpener) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.codexCLIResumeOpener = opener
}

// SetClaudeCLIResumeOpener 设置 Claude CLI resume 打开器，主要用于测试外部进程调用。
func (h *Handler) SetClaudeCLIResumeOpener(opener ClaudeCLIResumeOpener) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.claudeCLIResumeOpener = opener
}

func (h *Handler) resolveProgressConfig(agentName string) config.ProgressConfig {
	return h.resolveProgressConfigForPlatform("", agentName)
}

func (h *Handler) resolveProgressConfigForPlatform(platformName platform.PlatformName, agentName string) config.ProgressConfig {
	h.mu.RLock()
	global := h.progressConfig
	override, ok := h.agentProgressConfigs[agentName]
	platformOverride, platformOK := h.platformProgressConfigs[string(platformName)]
	h.mu.RUnlock()
	if global.Mode == "" {
		global = config.DefaultProgressConfig()
	}
	if ok {
		global = config.NormalizeProgressConfig(global, &override)
	}
	if platformOK {
		global = config.NormalizeProgressConfig(global, &platformOverride)
	}
	return normalizePlatformProgressConfig(platformName, global, platformOK)
}

// normalizePlatformProgressConfig 收敛平台默认进度体验，避免把不完整的 Agent delta 暴露给终端用户。
func normalizePlatformProgressConfig(platformName platform.PlatformName, cfg config.ProgressConfig, hasPlatformOverride bool) config.ProgressConfig {
	if platformName != platform.PlatformFeishu || hasPlatformOverride {
		return cfg
	}
	if cfg.Mode == progressModeOff {
		return cfg
	}
	cfg.Mode = progressModeSummary
	return cfg
}

func (h *Handler) defaultAgentNameForPlatform(platformName platform.PlatformName) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if agentName := h.platformDefaultAgents[string(platformName)]; agentName != "" {
		return agentName
	}
	return h.defaultName
}

// contextWithTaskTimeout 只限制 Agent 执行耗时，最终失败回复继续使用原始请求上下文发送。
func contextWithTaskTimeout(ctx context.Context, cfg config.ProgressConfig) (context.Context, context.CancelFunc) {
	if cfg.TaskTimeoutSeconds <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(cfg.TaskTimeoutSeconds)*time.Second)
}

func (h *Handler) ensureCodexSessions() *codexSessionStore {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.codexSessions == nil {
		h.codexSessions = newCodexSessionStore()
	}
	return h.codexSessions
}

// SetCodexSessionFile 设置 Codex workspace/thread 列表的持久化文件。
func (h *Handler) SetCodexSessionFile(filePath string) {
	h.ensureCodexSessions().SetFilePath(filePath)
}

func (h *Handler) ensureClaudeSessions() *claudeSessionStore {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.claudeSessions == nil {
		h.claudeSessions = newClaudeSessionStore()
	}
	return h.claudeSessions
}

// SetClaudeSessionFile 设置 Claude workspace/session 列表的持久化文件。
func (h *Handler) SetClaudeSessionFile(filePath string) {
	h.ensureClaudeSessions().SetFilePath(filePath)
}

// SetDefaultAgent sets the default agent (already started).
func (h *Handler) SetDefaultAgent(name string, ag agent.Agent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultName = name
	h.agents[name] = ag
	log.Printf("[handler] default agent ready: %s (%s)", name, ag.Info())
}

// AgentByName 返回已启动的 agent；软配置热重载只切换已存在实例。
func (h *Handler) AgentByName(name string) agent.Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.agents[name]
}

// EnsureAgentStarted 启动或复用指定 agent，供后台预热与消息按需启动共享同一条路径。
func (h *Handler) EnsureAgentStarted(ctx context.Context, name string) (agent.Agent, error) {
	return h.getAgent(ctx, name)
}

// getAgent returns a running agent by name, or starts it on demand via factory.
func (h *Handler) getAgent(ctx context.Context, name string) (agent.Agent, error) {
	// Fast path: already running
	h.mu.RLock()
	ag, ok := h.agents[name]
	h.mu.RUnlock()
	if ok {
		return ag, nil
	}

	// Slow path: create on demand
	if h.factory == nil {
		return nil, fmt.Errorf("agent %q not found and no factory configured", name)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if ag, ok := h.agents[name]; ok {
		return ag, nil
	}

	log.Printf("[handler] starting agent %q on demand...", name)
	ag = h.factory(ctx, name)
	if ag == nil {
		return nil, fmt.Errorf("agent %q not available", name)
	}

	h.agents[name] = ag
	log.Printf("[handler] agent started on demand: %s (%s)", name, ag.Info())
	return ag, nil
}

// getDefaultAgent returns the default agent (may be nil if not ready yet).
func (h *Handler) getDefaultAgent() agent.Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.defaultName == "" {
		return nil
	}
	return h.agents[h.defaultName]
}

// isKnownAgent checks if a name corresponds to a configured agent.
func (h *Handler) isKnownAgent(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	// Check running agents
	if _, ok := h.agents[name]; ok {
		return true
	}
	// Check configured agents (metas)
	for _, meta := range h.agentMetas {
		if meta.Name == name {
			return true
		}
	}
	return false
}

// agentAliases maps short aliases to agent config names.
var agentAliases = map[string]string{
	"cc":  "claude",
	"cx":  "codex",
	"oc":  "openclaw",
	"cs":  "cursor",
	"km":  "kimi",
	"gm":  "gemini",
	"ocd": "opencode",
	"pi":  "pi",
	"cp":  "copilot",
	"dr":  "droid",
	"if":  "iflow",
	"kr":  "kiro",
	"qw":  "qwen",
}

// resolveAlias returns the full agent name for an alias, or the original name if no alias matches.
// Checks custom aliases (from config) first, then built-in aliases.
func (h *Handler) resolveAlias(name string) string {
	h.mu.RLock()
	custom := h.customAliases
	h.mu.RUnlock()
	if custom != nil {
		if full, ok := custom[name]; ok {
			return full
		}
	}
	if full, ok := agentAliases[name]; ok {
		return full
	}
	return name
}

// parseCommand checks if text starts with "/" or "@" followed by agent name(s).
// Supports multiple agents: "@cc @cx hello" returns (["claude","codex"], "hello").
// Returns (agentNames, actualMessage). Aliases are resolved automatically.
// If no command prefix, returns (nil, originalText).
func (h *Handler) parseCommand(text string) ([]string, string) {
	if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "@") {
		return nil, text
	}

	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil, text
	}

	var names []string
	messageStart := len(text)
	for _, field := range fields {
		if !strings.HasPrefix(field, "/") && !strings.HasPrefix(field, "@") {
			messageStart = strings.Index(text, field)
			break
		}

		token, ok := h.parseAgentToken(field)
		if !ok {
			return nil, text
		}
		names = append(names, token)
		messageStart = len(text)
	}

	rest := strings.TrimSpace(text[messageStart:])
	seen := make(map[string]bool)
	unique := names[:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			unique = append(unique, n)
		}
	}

	return unique, rest
}

// parseAgentToken 只接受独立的 /agent 或 @agent token，避免把绝对路径拆成多个 Agent。
func (h *Handler) parseAgentToken(field string) (string, bool) {
	if len(field) <= 1 {
		return "", false
	}
	token := field[1:]
	if strings.ContainsAny(token, "/@") {
		return "", false
	}
	return h.resolveAlias(token), true
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

	text := strings.TrimSpace(platformMessageText(msg))
	if msg.RawCommand != nil && msg.RawCommand.Action == "stop" {
		sendPlatformText(ctx, replyWriter, msg.UserID, h.handleStopActiveTask(ctx, msg.UserID))
		return
	}
	if msg.RawCommand != nil && msg.RawCommand.Action == "choice" && h.consumePendingApproval(msg.UserID, msg.RawCommand.Value["choice"]) {
		return
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

	if h.handleBuiltInPlatformCommand(ctx, msg, replyWriter, trimmed, clientID) {
		return
	}

	agentNames, message := h.parseCommand(text)
	if len(agentNames) == 0 {
		h.sendToDefaultAgent(ctx, msg.Platform, msg.UserID, replyWriter, text, clientID)
		return
	}
	if message == "" {
		if len(agentNames) == 1 && h.isKnownAgent(agentNames[0]) {
			sendText(h.switchDefault(ctx, agentNames[0]))
		} else if len(agentNames) == 1 && !h.isKnownAgent(agentNames[0]) {
			h.sendToDefaultAgent(ctx, msg.Platform, msg.UserID, replyWriter, text, clientID)
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
		h.sendToDefaultAgent(ctx, msg.Platform, msg.UserID, replyWriter, text, clientID)
		return
	}
	if len(knownNames) == 1 {
		h.sendToNamedAgent(ctx, msg.Platform, msg.UserID, replyWriter, knownNames[0], message, clientID)
		return
	}
	h.broadcastToAgents(ctx, msg.Platform, msg.UserID, replyWriter, knownNames, message)
}

func (h *Handler) handleBuiltInPlatformCommand(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier, trimmed string, clientID string) bool {
	sendText := func(text string) {
		sendPlatformText(ctx, reply, msg.UserID, text)
	}
	switch {
	case trimmed == "/status":
		sendText(h.buildStatus())
	case trimmed == "/info":
		sendText("命令已移除，请使用 /status 查看 WeClaw 全局运行态。")
	case trimmed == "/help":
		sendText(buildHelpText())
	case trimmed == "/new" || trimmed == "/clear":
		sendText(h.resetDefaultSession(ctx, msg.UserID))
	case isRemovedSwitchCommand(trimmed):
		sendText("命令已移除：WeClaw 不再支持从微信端切换 Codex 账号。")
	case isProgressCommand(trimmed):
		sendText(h.handleProgressCommandForPlatform(trimmed, msg.Platform))
	case isClaudeSessionCommand(trimmed):
		sendText(h.handleClaudeSessionCommand(ctx, msg.UserID, trimmed))
	case isCodexSessionCommand(trimmed):
		if h.handleFeishuCodexSessionCommand(ctx, msg, reply, trimmed) {
			return true
		}
		sendText(h.handleCodexSessionCommand(ctx, msg.UserID, trimmed))
	case trimmed == "/run":
		h.handleRunPendingCodexCommand(ctx, msg.Platform, msg.UserID, reply, clientID)
	case trimmed == "/guide":
		h.handleGuideCommand(ctx, msg.Platform, msg.UserID, reply, clientID)
	case trimmed == "/cancel":
		sendText(h.handleCancelPendingGuide(ctx, msg.UserID))
	case strings.HasPrefix(trimmed, "/cwd"):
		sendText(h.handleCwd(trimmed, msg.UserID))
	default:
		return false
	}
	return true
}

func platformMessageText(msg platform.IncomingMessage) string {
	if msg.RawCommand != nil && msg.RawCommand.Action == "choice" {
		return strings.TrimSpace(msg.RawCommand.Value["choice"])
	}
	return msg.Text
}

func platformMessageDedupKey(msg platform.IncomingMessage) string {
	return strings.TrimSpace(string(msg.Platform)) + "\x00" + strings.TrimSpace(msg.AccountID) + "\x00" + strings.TrimSpace(msg.MessageID)
}

func (h *Handler) agentExecutionKey(userID string, agentName string, ag agent.Agent) string {
	info := ag.Info()
	if isCodexAgent(agentName, info) {
		return h.codexConversationRouteForUser(userID, agentName, ag).conversationID
	}
	if isClaudeAgent(agentName, info) {
		workspaceRoot := h.claudeWorkspaceRootForUser(userID, agentName, ag)
		return buildClaudeConversationID(userID, agentName, workspaceRoot)
	}
	return strings.Join([]string{"agent", strings.TrimSpace(userID), strings.TrimSpace(agentName)}, "\x00")
}

func (h *Handler) codexConversationRouteForUser(userID string, agentName string, ag agent.Agent) codexConversationRoute {
	workspaceRoot := h.codexWorkspaceRootForUser(userID, agentName, ag)
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	return codexConversationRoute{
		bindingKey:     codexBindingKey(userID, agentName),
		workspaceRoot:  workspaceRoot,
		conversationID: conversationID,
	}
}

func (h *Handler) bindConversationCwd(ag agent.Agent, conversationID string, workspaceRoot string) {
	if workspaceAg, ok := ag.(agent.ConversationWorkspaceAgent); ok {
		workspaceAg.SetConversationCwd(conversationID, workspaceRoot)
	}
}

func (h *Handler) lockAgentExecution(key string) func() {
	h.taskLocksMu.Lock()
	if h.taskLocks == nil {
		h.taskLocks = make(map[string]*sync.Mutex)
	}
	lock := h.taskLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		h.taskLocks[key] = lock
	}
	h.taskLocksMu.Unlock()

	// 同一执行通道串行进入，避免 Codex 同一 thread 内并发 turn 串结果。
	lock.Lock()
	return lock.Unlock
}

func (h *Handler) beginActiveTask(ctx context.Context, key string) (*activeAgentTask, context.Context, bool) {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	if h.activeTasks == nil {
		h.activeTasks = make(map[string]*activeAgentTask)
	}
	if h.activeTasks[key] != nil {
		return h.activeTasks[key], ctx, false
	}
	taskCtx, cancel := context.WithCancel(ctx)
	task := &activeAgentTask{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	h.activeTasks[key] = task
	return task, taskCtx, true
}

func (h *Handler) finishActiveTask(key string, task *activeAgentTask) {
	h.activeTasksMu.Lock()
	if h.activeTasks[key] == task {
		delete(h.activeTasks, key)
	}
	h.activeTasksMu.Unlock()
	close(task.done)
}

func (h *Handler) storePendingGuide(key string, message string) bool {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	h.activeTasksMu.Unlock()
	if task == nil {
		return false
	}
	task.mu.Lock()
	task.pendingMessage = message
	task.mu.Unlock()
	return true
}

func (h *Handler) detachPendingGuide(key string) (string, *activeAgentTask, bool) {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return "", nil, false
	}

	task.mu.Lock()
	message := task.pendingMessage
	if message == "" {
		task.mu.Unlock()
		h.activeTasksMu.Unlock()
		return "", nil, false
	}
	task.pendingMessage = ""
	task.detached = true
	cancel := task.cancel
	task.mu.Unlock()
	h.activeTasksMu.Unlock()
	cancel()
	return message, task, true
}

func (h *Handler) clearPendingGuide(key string) bool {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	h.activeTasksMu.Unlock()
	if task == nil {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.pendingMessage == "" {
		return false
	}
	task.pendingMessage = ""
	return true
}

// promotePendingGuideToRun 将未处理的引导消息转为待执行消息，避免任务结束后丢失用户输入。
func (h *Handler) promotePendingGuideToRun(key string, task *activeAgentTask) (string, bool) {
	if task == nil {
		return "", false
	}
	task.mu.Lock()
	if task.detached || task.pendingMessage == "" {
		task.mu.Unlock()
		return "", false
	}
	message := task.pendingMessage
	task.pendingMessage = ""
	task.mu.Unlock()
	h.storePendingCodexRun(key, message)
	return message, true
}

// storePendingCodexRun 保存等待用户用 /run 明确确认的 Codex 消息。
func (h *Handler) storePendingCodexRun(key string, message string) {
	h.pendingCodexRunsMu.Lock()
	if h.pendingCodexRuns == nil {
		h.pendingCodexRuns = make(map[string]string)
	}
	h.pendingCodexRuns[key] = message
	h.pendingCodexRunsMu.Unlock()
}

// takePendingCodexRun 取出并删除待执行消息，保证 /run 不会重复执行同一条输入。
func (h *Handler) takePendingCodexRun(key string) (string, bool) {
	h.pendingCodexRunsMu.Lock()
	defer h.pendingCodexRunsMu.Unlock()
	message := h.pendingCodexRuns[key]
	if message == "" {
		return "", false
	}
	delete(h.pendingCodexRuns, key)
	return message, true
}

// clearPendingCodexRun 撤回已经转为待执行状态的 Codex 消息。
func (h *Handler) clearPendingCodexRun(key string) bool {
	h.pendingCodexRunsMu.Lock()
	defer h.pendingCodexRunsMu.Unlock()
	if h.pendingCodexRuns[key] == "" {
		return false
	}
	delete(h.pendingCodexRuns, key)
	return true
}

func (h *Handler) approvalHandlerForUser(userID string, reply platform.Replier) agent.ApprovalHandler {
	return func(ctx context.Context, req agent.ApprovalRequest) (string, error) {
		choices := approvalChoices(req.Options)
		if len(choices) == 0 {
			return "", fmt.Errorf("approval request has no options")
		}
		pending := h.registerPendingApproval(userID, req.Options)
		defer h.clearPendingApproval(userID, pending)
		if err := reply.AskChoices(ctx, approvalPrompt(req), choices); err != nil {
			return "", err
		}
		timer := time.NewTimer(pendingApprovalTimeout)
		defer timer.Stop()
		select {
		case choice := <-pending.choices:
			return strings.TrimSpace(choice), nil
		case <-timer.C:
			return defaultDenyApprovalOption(req.Options), nil
		case <-ctx.Done():
			return defaultDenyApprovalOption(req.Options), ctx.Err()
		}
	}
}

func (h *Handler) registerPendingApproval(userID string, options []agent.ApprovalOption) *pendingApproval {
	pending := &pendingApproval{choices: make(chan string, 1), allowed: approvalOptionSet(options)}
	h.pendingApprovalsMu.Lock()
	if h.pendingApprovals == nil {
		h.pendingApprovals = make(map[string]*pendingApproval)
	}
	h.pendingApprovals[userID] = pending
	h.pendingApprovalsMu.Unlock()
	return pending
}

func (h *Handler) clearPendingApproval(userID string, pending *pendingApproval) {
	h.pendingApprovalsMu.Lock()
	if h.pendingApprovals[userID] == pending {
		delete(h.pendingApprovals, userID)
	}
	h.pendingApprovalsMu.Unlock()
}

func (h *Handler) consumePendingApproval(userID string, choice string) bool {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return false
	}
	h.pendingApprovalsMu.Lock()
	pending := h.pendingApprovals[userID]
	h.pendingApprovalsMu.Unlock()
	if pending == nil {
		return false
	}
	if len(pending.allowed) > 0 && !pending.allowed[choice] {
		return false
	}
	select {
	case pending.choices <- choice:
	default:
	}
	return true
}

func approvalOptionSet(options []agent.ApprovalOption) map[string]bool {
	allowed := make(map[string]bool, len(options))
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id != "" {
			allowed[id] = true
		}
	}
	return allowed
}

func approvalPrompt(req agent.ApprovalRequest) string {
	toolCall := strings.TrimSpace(string(req.ToolCall))
	if toolCall == "" {
		toolCall = "Codex 请求执行一项需要确认的操作。"
	} else if len([]rune(toolCall)) > 1200 {
		runes := []rune(toolCall)
		toolCall = string(runes[:1200]) + "..."
	}
	return "Codex 请求执行敏感操作，请确认：\n\n" + toolCall
}

func approvalChoices(options []agent.ApprovalOption) []platform.Choice {
	choices := make([]platform.Choice, 0, len(options))
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		choices = append(choices, platform.Choice{ID: id, Label: approvalChoiceLabel(option)})
	}
	return choices
}

func approvalChoiceLabel(option agent.ApprovalOption) string {
	switch option.Kind {
	case "allow":
		return "允许本次"
	case "deny", "reject":
		return "拒绝"
	default:
		return firstNonBlank(option.Name, option.Kind, option.ID)
	}
}

func defaultDenyApprovalOption(options []agent.ApprovalOption) string {
	for _, option := range options {
		if option.Kind != "allow" && strings.TrimSpace(option.ID) != "" {
			return option.ID
		}
	}
	if len(options) > 0 {
		return options[0].ID
	}
	return ""
}

func (t *activeAgentTask) shouldSendFinal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.detached
}

func runningCodexGuidePrompt() string {
	return "Codex 正在处理上一条任务。\n\n回复 /guide 将此消息作为引导对话发送给 Codex。\n回复 /cancel 撤回该消息。\n不回复时，上一条任务完成后会转为待执行消息。"
}

// runnablePendingCodexPrompt 提醒用户确认执行已从引导态转出的暂存消息。
func runnablePendingCodexPrompt(message string) string {
	return "上一条 Codex 任务已完成。\n\n暂存消息：\n" + previewPendingCodexMessage(message) + "\n\n回复 /run 执行该消息。\n回复 /cancel 撤回该消息。"
}

// previewPendingCodexMessage 限制微信提示里的消息预览长度，避免长输入刷屏。
func previewPendingCodexMessage(message string) string {
	runes := []rune(strings.TrimSpace(message))
	if len(runes) <= pendingCodexPreviewRunes {
		return string(runes)
	}
	return string(runes[:pendingCodexPreviewRunes]) + "..."
}

// handleRunPendingCodexCommand 执行用户确认后的待执行 Codex 消息。
func (h *Handler) handleRunPendingCodexCommand(ctx context.Context, platformName platform.PlatformName, userID string, reply platform.Replier, clientID string) {
	name, _, key, err := h.codexGuideTarget(ctx, userID)
	if err != nil {
		sendPlatformText(ctx, reply, userID, err.Error())
		return
	}
	message, ok := h.takePendingCodexRun(key)
	if !ok {
		sendPlatformText(ctx, reply, userID, "当前没有待执行的暂存消息。")
		return
	}
	h.sendToNamedAgent(ctx, platformName, userID, reply, name, message, clientID)
}

func (h *Handler) handleGuideCommand(ctx context.Context, platformName platform.PlatformName, userID string, reply platform.Replier, clientID string) {
	name, _, key, err := h.codexGuideTarget(ctx, userID)
	if err != nil {
		sendPlatformText(ctx, reply, userID, err.Error())
		return
	}
	message, task, ok := h.detachPendingGuide(key)
	if !ok {
		sendPlatformText(ctx, reply, userID, "当前没有可发送的引导对话。")
		return
	}
	if !waitForActiveTask(ctx, task) {
		return
	}
	h.sendToNamedAgent(ctx, platformName, userID, reply, name, message, clientID)
}

func (h *Handler) handleCancelPendingGuide(ctx context.Context, userID string) string {
	_, _, key, err := h.codexGuideTarget(ctx, userID)
	if err != nil {
		return err.Error()
	}
	if !h.clearPendingGuide(key) && !h.clearPendingCodexRun(key) {
		return "当前没有可撤回的消息。"
	}
	return "已撤回该消息。"
}

func (h *Handler) handleStopActiveTask(ctx context.Context, userID string) string {
	_, _, key, err := h.codexGuideTarget(ctx, userID)
	if err != nil {
		return err.Error()
	}
	if !h.cancelActiveTask(key) {
		return "当前没有可停止的任务。"
	}
	return "已停止当前任务。"
}

func (h *Handler) cancelActiveTask(key string) bool {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	h.activeTasksMu.Unlock()
	if task == nil {
		return false
	}
	task.mu.Lock()
	task.pendingMessage = ""
	task.detached = true
	cancel := task.cancel
	task.mu.Unlock()
	cancel()
	return true
}

func (h *Handler) codexGuideTarget(ctx context.Context, userID string) (string, agent.Agent, string, error) {
	name, ok := h.codexAgentName()
	if !ok {
		return "", nil, "", fmt.Errorf("当前没有配置 Codex Agent。")
	}
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		return "", nil, "", fmt.Errorf("Codex Agent 不可用: %v", err)
	}
	return name, ag, h.agentExecutionKey(userID, name, ag), nil
}

func waitForActiveTask(ctx context.Context, task *activeAgentTask) bool {
	if task == nil {
		return true
	}
	select {
	case <-task.done:
		return true
	case <-ctx.Done():
		return false
	}
}

// sendToDefaultAgent sends the message to the default agent and replies.
func (h *Handler) sendToDefaultAgent(ctx context.Context, platformName platform.PlatformName, userID string, replyWriter platform.Replier, text string, clientID string) {
	defaultName := h.defaultAgentNameForPlatform(platformName)

	var ag agent.Agent
	var agErr error
	if defaultName != "" {
		ag, agErr = h.getAgent(ctx, defaultName)
	}
	var reply string
	if defaultName != "" && agErr == nil {
		progressCfg := h.resolveProgressConfigForPlatform(platformName, defaultName)
		if isCodexAgent(defaultName, ag.Info()) {
			h.startCodexAgentTask(codexAgentTaskOptions{
				ctx:         ctx,
				userID:      userID,
				reply:       replyWriter,
				agentName:   defaultName,
				message:     text,
				clientID:    clientID,
				replyPrefix: "",
				agent:       ag,
				progressCfg: progressCfg,
			})
			return
		}

		replyCtx := ctx
		agentCtx, cancelTaskTimeout := contextWithTaskTimeout(ctx, progressCfg)
		defer cancelTaskTimeout()
		agentCtx = agent.ContextWithApprovalHandler(agentCtx, h.approvalHandlerForUser(userID, replyWriter))

		executionKey := h.agentExecutionKey(userID, defaultName, ag)
		unlock := h.lockAgentExecution(executionKey)
		defer unlock()

		onProgress, finishProgress := h.startProgressSessionWithFinal(agentCtx, replyWriter, "", text, progressCfg)

		var err error
		conversationID, resolveErr := h.resolveAgentConversationID(agentCtx, userID, defaultName, ag)
		if resolveErr != nil {
			reply = renderFinalFailure("", resolveErr)
			consumed := finishProgressWithReply(finishProgress, reply, true)
			h.sendReplyWithMediaAfterStream(replyCtx, replyWriter, userID, defaultName, reply, consumed)
			return
		}
		reply, err = h.chatWithAgentWithProgress(agentCtx, ag, conversationID, text, onProgress)
		if err != nil {
			reply = renderFinalFailure("", err)
		} else {
			h.recordCodexThread(userID, defaultName, ag, conversationID)
			h.recordClaudeSession(userID, defaultName, ag, conversationID)
			reply = renderFinalSuccess("", reply)
		}
		consumed := finishProgressWithReply(finishProgress, reply, err != nil)
		h.sendReplyWithMediaAfterStream(replyCtx, replyWriter, userID, defaultName, reply, consumed)
		return
	} else {
		if agErr != nil && defaultName != "" {
			log.Printf("[handler] default agent %q not available, using echo mode for %s: %v", defaultName, userID, agErr)
		}
		log.Printf("[handler] agent not ready, using echo mode for %s", userID)
		reply = "[echo] " + text
	}

	h.sendReplyWithMedia(ctx, replyWriter, userID, defaultName, reply)
}

// sendToNamedAgent sends the message to a specific agent and replies.
func (h *Handler) sendToNamedAgent(ctx context.Context, platformName platform.PlatformName, userID string, replyWriter platform.Replier, name string, message string, clientID string) {
	ag, agErr := h.getAgent(ctx, name)
	if agErr != nil {
		log.Printf("[handler] agent %q not available: %v", name, agErr)
		reply := fmt.Sprintf("Agent %q is not available: %v", name, agErr)
		sendPlatformText(ctx, replyWriter, userID, reply)
		return
	}

	replyCtx := ctx
	progressCfg := h.resolveProgressConfigForPlatform(platformName, name)
	if isCodexAgent(name, ag.Info()) {
		h.startCodexAgentTask(codexAgentTaskOptions{
			ctx:         ctx,
			userID:      userID,
			reply:       replyWriter,
			agentName:   name,
			message:     message,
			clientID:    clientID,
			replyPrefix: "[" + name + "] ",
			agent:       ag,
			progressCfg: progressCfg,
		})
		return
	}

	agentCtx, cancelTaskTimeout := contextWithTaskTimeout(ctx, progressCfg)
	defer cancelTaskTimeout()
	agentCtx = agent.ContextWithApprovalHandler(agentCtx, h.approvalHandlerForUser(userID, replyWriter))

	executionKey := h.agentExecutionKey(userID, name, ag)
	unlock := h.lockAgentExecution(executionKey)
	defer unlock()

	onProgress, finishProgress := h.startProgressSessionWithFinal(agentCtx, replyWriter, "", message, progressCfg)

	conversationID, resolveErr := h.resolveAgentConversationID(agentCtx, userID, name, ag)
	if resolveErr != nil {
		reply := renderFinalFailure("["+name+"] ", resolveErr)
		consumed := finishProgressWithReply(finishProgress, reply, true)
		h.sendReplyWithMediaAfterStream(replyCtx, replyWriter, userID, name, reply, consumed)
		return
	}
	reply, err := h.chatWithAgentWithProgress(agentCtx, ag, conversationID, message, onProgress)
	if err != nil {
		reply = renderFinalFailure("["+name+"] ", err)
	} else {
		h.recordCodexThread(userID, name, ag, conversationID)
		h.recordClaudeSession(userID, name, ag, conversationID)
		reply = renderFinalSuccess("["+name+"] ", reply)
	}
	consumed := finishProgressWithReply(finishProgress, reply, err != nil)
	h.sendReplyWithMediaAfterStream(replyCtx, replyWriter, userID, name, reply, consumed)
}

// startCodexAgentTask 先登记 active task 再后台执行，保证 /guide 和 /cancel 可及时进入 Handler。
func (h *Handler) startCodexAgentTask(opts codexAgentTaskOptions) {
	agentCtx, cancelTaskTimeout := contextWithTaskTimeout(opts.ctx, opts.progressCfg)
	agentCtx = agent.ContextWithApprovalHandler(agentCtx, h.approvalHandlerForUser(opts.userID, opts.reply))
	route := h.codexConversationRouteForUser(opts.userID, opts.agentName, opts.agent)
	executionKey := route.conversationID
	task, taskCtx, started := h.beginActiveTask(agentCtx, executionKey)
	if !started {
		cancelTaskTimeout()
		h.storePendingGuide(executionKey, opts.message)
		sendPlatformText(opts.ctx, opts.reply, opts.userID, runningCodexGuidePrompt())
		return
	}

	go h.runCodexAgentTask(codexAgentTaskRuntime{
		opts:              opts,
		agentCtx:          taskCtx,
		cancelTaskTimeout: cancelTaskTimeout,
		executionKey:      executionKey,
		route:             route,
		task:              task,
	})
}

// runCodexAgentTask 在后台完成 Codex 调用和最终回复发送。
func (h *Handler) runCodexAgentTask(runtime codexAgentTaskRuntime) {
	opts := runtime.opts
	defer h.finishCodexAgentTask(runtime)

	unlock := h.lockAgentExecution(runtime.executionKey)
	defer unlock()

	onProgress, finishProgress := h.startProgressSessionWithFinal(runtime.agentCtx, opts.reply, opts.replyPrefix, opts.message, opts.progressCfg)

	if err := h.prepareCodexConversation(runtime.agentCtx, runtime.route, opts.agent); err != nil {
		reply := renderFinalFailure(opts.replyPrefix, err)
		consumed := finishProgressWithReply(finishProgress, reply, true)
		h.sendReplyWithMediaAfterStream(opts.ctx, opts.reply, opts.userID, opts.agentName, reply, consumed)
		return
	}
	reply, err := h.chatWithAgentWithProgress(runtime.agentCtx, opts.agent, runtime.route.conversationID, opts.message, onProgress)
	if err != nil {
		reply = renderFinalFailure(opts.replyPrefix, err)
	} else {
		h.recordCodexThreadForWorkspace(opts.userID, opts.agentName, opts.agent, runtime.route.conversationID, runtime.route.workspaceRoot)
		reply = renderFinalSuccess(opts.replyPrefix, reply)
	}
	if runtime.task.shouldSendFinal() {
		consumed := finishProgressWithReply(finishProgress, reply, err != nil)
		h.sendReplyWithMediaAfterStream(opts.ctx, opts.reply, opts.userID, opts.agentName, reply, consumed)
	} else {
		_ = finishProgress("", false)
	}
}

// finishCodexAgentTask 收尾后台任务，并把未处理的暂存引导转成 /run 待确认消息。
func (h *Handler) finishCodexAgentTask(runtime codexAgentTaskRuntime) {
	runtime.cancelTaskTimeout()
	message, ok := h.promotePendingGuideToRun(runtime.executionKey, runtime.task)
	h.finishActiveTask(runtime.executionKey, runtime.task)
	if !ok {
		return
	}
	opts := runtime.opts
	sendPlatformText(opts.ctx, opts.reply, opts.userID, runnablePendingCodexPrompt(message))
}

// broadcastToAgents sends the message to multiple agents in parallel.
// Each reply is sent as a separate message with the agent name prefix.
func (h *Handler) broadcastToAgents(ctx context.Context, platformName platform.PlatformName, userID string, replyWriter platform.Replier, names []string, message string) {
	type result struct {
		name          string
		reply         string
		skip          bool
		finalInStream bool
	}

	ch := make(chan result, len(names))

	for _, name := range names {
		go func(n string) {
			ag, err := h.getAgent(ctx, n)
			if err != nil {
				ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err)}
				return
			}
			progressCfg := h.resolveProgressConfigForPlatform(platformName, n)
			agentCtx, cancelTaskTimeout := contextWithTaskTimeout(ctx, progressCfg)
			defer cancelTaskTimeout()
			agentCtx = agent.ContextWithApprovalHandler(agentCtx, h.approvalHandlerForUser(userID, replyWriter))

			var codexRoute codexConversationRoute
			var executionKey string
			var activeTask *activeAgentTask
			if isCodexAgent(n, ag.Info()) {
				codexRoute = h.codexConversationRouteForUser(userID, n, ag)
				executionKey = codexRoute.conversationID
				task, taskCtx, started := h.beginActiveTask(agentCtx, executionKey)
				if !started {
					h.storePendingGuide(executionKey, message)
					ch <- result{name: n, reply: runningCodexGuidePrompt()}
					return
				}
				activeTask = task
				agentCtx = taskCtx
				defer func() {
					pendingMessage, ok := h.promotePendingGuideToRun(executionKey, task)
					h.finishActiveTask(executionKey, task)
					if ok {
						sendPlatformText(ctx, replyWriter, userID, runnablePendingCodexPrompt(pendingMessage))
					}
				}()
			} else {
				executionKey = h.agentExecutionKey(userID, n, ag)
			}
			unlock := h.lockAgentExecution(executionKey)
			defer unlock()

			onProgress, finishProgress := h.startProgressSessionWithFinal(agentCtx, replyWriter, "["+n+"] ", message, progressCfg)
			sendResult := func(reply string, failed bool) {
				consumed := finishProgressWithReply(finishProgress, reply, failed)
				ch <- result{name: n, reply: reply, finalInStream: consumed}
			}

			var conversationID string
			if isCodexAgent(n, ag.Info()) {
				if err := h.prepareCodexConversation(agentCtx, codexRoute, ag); err != nil {
					sendResult(renderFinalFailure("["+n+"] ", err), true)
					return
				}
				conversationID = codexRoute.conversationID
			} else {
				resolvedID, resolveErr := h.resolveAgentConversationID(agentCtx, userID, n, ag)
				if resolveErr != nil {
					sendResult(renderFinalFailure("["+n+"] ", resolveErr), true)
					return
				}
				conversationID = resolvedID
			}
			reply, err := h.chatWithAgentWithProgress(agentCtx, ag, conversationID, message, onProgress)
			if err != nil {
				sendResult(renderFinalFailure("["+n+"] ", err), true)
				return
			}
			if isCodexAgent(n, ag.Info()) {
				h.recordCodexThreadForWorkspace(userID, n, ag, conversationID, codexRoute.workspaceRoot)
			} else {
				h.recordCodexThread(userID, n, ag, conversationID)
				h.recordClaudeSession(userID, n, ag, conversationID)
			}
			if activeTask != nil && !activeTask.shouldSendFinal() {
				_ = finishProgress("", false)
				ch <- result{name: n, skip: true}
				return
			}
			sendResult(renderFinalSuccess("["+n+"] ", reply), false)
		}(name)
	}

	// Send replies as they arrive
	for range names {
		r := <-ch
		if r.skip {
			continue
		}
		if wxReply, ok := replyWriter.(*wechat.Replier); ok {
			wxReply.ClientID = NewClientID()
		}
		h.sendReplyWithMediaAfterStream(ctx, replyWriter, userID, r.name, r.reply, r.finalInStream)
	}
}

// sendReplyWithMedia sends a text reply and any extracted image URLs.
func (h *Handler) sendReplyWithMedia(ctx context.Context, replyWriter platform.Replier, userID string, agentName string, reply string) {
	h.sendReplyWithMediaAfterStream(ctx, replyWriter, userID, agentName, reply, false)
}

func (h *Handler) sendReplyWithMediaAfterStream(ctx context.Context, replyWriter platform.Replier, userID string, agentName string, reply string, finalInStream bool) {
	imageURLs := ExtractImageURLs(reply)
	attachmentPaths := extractLocalAttachmentPaths(reply)
	allowedRoots := h.allowedAttachmentRoots(agentName)

	var sentPaths []string
	var failedPaths []string
	for _, attachmentPath := range attachmentPaths {
		if !isAllowedAttachmentPath(attachmentPath, allowedRoots) {
			log.Printf("[handler] rejected attachment outside allowed roots for agent %q: %s", agentName, attachmentPath)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		if err := replyWriter.SendImage(ctx, attachmentPath); err != nil {
			log.Printf("[handler] failed to send attachment to %s: %v", userID, err)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
	}

	reply = rewriteReplyWithAttachmentResults(reply, sentPaths, failedPaths)
	choiceResult, hasChoices := detectChoices(reply)
	if hasChoices {
		reply = choiceResult.CleanText
	}

	if wxReply, ok := replyWriter.(*wechat.Replier); ok {
		wxReply.ChunkRunes = textReplyChunkLimit(ctx)
	}
	if !finalInStream && strings.TrimSpace(reply) != "" {
		if err := replyWriter.SendText(ctx, reply); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", userID, err)
		}
	}
	if hasChoices {
		if err := replyWriter.AskChoices(ctx, choiceResult.Prompt, choiceResult.Choices); err != nil {
			log.Printf("[handler] failed to send choices to %s: %v", userID, err)
		}
	}

	for _, imgURL := range imageURLs {
		if wxReply, ok := replyWriter.(*wechat.Replier); ok {
			if err := wxReply.SendMediaFromURL(ctx, imgURL); err != nil {
				log.Printf("[handler] failed to send image to %s: %v", userID, err)
			}
			continue
		}
		log.Printf("[handler] skip remote image for %s: platform replier has no URL media sender", userID)
	}
}

func finalCardReplyText(reply string) string {
	choiceResult, hasChoices := detectChoices(reply)
	if hasChoices {
		return choiceResult.CleanText
	}
	return reply
}

func finishProgressWithReply(finish func(string, bool) bool, reply string, failed bool) bool {
	if !canConsumeFinalReplyInStream(reply) {
		_ = finish("", failed)
		return false
	}
	return finish(finalCardReplyText(reply), failed)
}

func canConsumeFinalReplyInStream(reply string) bool {
	return len(ExtractImageURLs(reply)) == 0 && len(extractLocalAttachmentPaths(reply)) == 0
}

func sendPlatformText(ctx context.Context, reply platform.Replier, userID string, text string) {
	if reply == nil {
		return
	}
	if err := reply.SendText(ctx, text); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", userID, err)
	}
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func (h *Handler) allowedAttachmentRoots(agentName string) []string {
	roots := []string{defaultAttachmentWorkspace()}

	h.mu.RLock()
	agentDir := h.agentWorkDirs[agentName]
	h.mu.RUnlock()

	if agentDir != "" {
		roots = append(roots, agentDir)
	}

	return roots
}

func (h *Handler) resolveAgentConversationID(ctx context.Context, userID string, agentName string, ag agent.Agent) (string, error) {
	if isCodexAgent(agentName, ag.Info()) {
		return h.resolveCodexConversationID(ctx, userID, agentName, ag)
	}
	if isClaudeAgent(agentName, ag.Info()) {
		return h.resolveClaudeConversationID(ctx, userID, agentName, ag)
	}
	return userID, nil
}

func (h *Handler) resolveCodexConversationID(ctx context.Context, userID string, agentName string, ag agent.Agent) (string, error) {
	route := h.codexConversationRouteForUser(userID, agentName, ag)
	if err := h.prepareCodexConversation(ctx, route, ag); err != nil {
		return "", err
	}
	return route.conversationID, nil
}

func (h *Handler) prepareCodexConversation(ctx context.Context, route codexConversationRoute, ag agent.Agent) error {
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		h.ensureCodexSessions().ensureWorkspace(route.bindingKey, route.workspaceRoot)
		return nil
	}
	threadID, pending := h.ensureCodexSessions().getThread(route.bindingKey, route.workspaceRoot)
	if pending {
		codexAg.ClearCodexThread(route.conversationID)
		return nil
	}
	if threadID != "" {
		current, hasCurrent := codexAg.CurrentCodexThread(route.conversationID)
		if !hasCurrent || current != threadID {
			if err := codexAg.UseCodexThread(ctx, route.conversationID, threadID); err != nil {
				return fmt.Errorf("恢复 Codex 会话失败: %w", err)
			}
		}
	}
	h.ensureCodexSessions().ensureWorkspace(route.bindingKey, route.workspaceRoot)
	return nil
}

func (h *Handler) resolveClaudeConversationID(ctx context.Context, userID string, agentName string, ag agent.Agent) (string, error) {
	workspaceRoot := h.claudeWorkspaceRootForUser(userID, agentName, ag)
	bindingKey := claudeBindingKey(userID, agentName)
	conversationID := buildClaudeConversationID(userID, agentName, workspaceRoot)
	claudeAg, ok := ag.(agent.ClaudeSessionAgent)
	if !ok {
		h.ensureClaudeSessions().ensureWorkspace(bindingKey, workspaceRoot)
		return conversationID, nil
	}
	sessionID, pending := h.ensureClaudeSessions().getSession(bindingKey, workspaceRoot)
	if pending {
		claudeAg.ClearClaudeSession(conversationID)
		return conversationID, nil
	}
	if sessionID != "" {
		current, hasCurrent := claudeAg.CurrentClaudeSession(conversationID)
		if !hasCurrent || current != sessionID {
			if err := claudeAg.UseClaudeSession(ctx, conversationID, sessionID); err != nil {
				return "", fmt.Errorf("恢复 Claude 会话失败: %w", err)
			}
		}
	}
	h.ensureClaudeSessions().ensureWorkspace(bindingKey, workspaceRoot)
	return conversationID, nil
}

func (h *Handler) recordCodexThread(userID string, agentName string, ag agent.Agent, conversationID string) {
	workspaceRoot := h.codexWorkspaceRootForUser(userID, agentName, ag)
	if recordedWorkspace, ok := h.recordCodexThreadForWorkspace(userID, agentName, ag, conversationID, workspaceRoot); ok {
		h.ensureCodexSessions().setActiveWorkspace(codexBindingKey(userID, agentName), recordedWorkspace)
	}
}

func (h *Handler) recordCodexThreadForWorkspace(userID string, agentName string, ag agent.Agent, conversationID string, workspaceRoot string) (string, bool) {
	if !isCodexAgent(agentName, ag.Info()) {
		return "", false
	}
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		return "", false
	}
	threadID, ok := codexAg.CurrentCodexThread(conversationID)
	if !ok {
		return "", false
	}
	bindingKey := codexBindingKey(userID, agentName)
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	if ownerWorkspace, ok := h.ensureCodexSessions().findWorkspaceByThread(bindingKey, threadID); ok {
		workspaceRoot = ownerWorkspace
	}
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
	return workspaceRoot, true
}

func (h *Handler) recordClaudeSession(userID string, agentName string, ag agent.Agent, conversationID string) {
	if !isClaudeAgent(agentName, ag.Info()) {
		return
	}
	claudeAg, ok := ag.(agent.ClaudeSessionAgent)
	if !ok {
		return
	}
	sessionID, ok := claudeAg.CurrentClaudeSession(conversationID)
	if !ok {
		return
	}
	workspaceRoot := h.claudeWorkspaceRootForUser(userID, agentName, ag)
	bindingKey := claudeBindingKey(userID, agentName)
	if ownerWorkspace, ok := h.ensureClaudeSessions().findWorkspaceBySession(bindingKey, sessionID); ok {
		workspaceRoot = ownerWorkspace
	}
	h.ensureClaudeSessions().setSession(bindingKey, workspaceRoot, sessionID)
	h.ensureClaudeSessions().setActiveWorkspace(bindingKey, workspaceRoot)
}

func (h *Handler) syncCodexThreadFromAgent(userID string, agentName string, workspaceRoot string, ag agent.Agent) {
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		return
	}
	bindingKey := codexBindingKey(userID, agentName)
	if _, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot); pending {
		return
	}
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
	threadID, ok := codexAg.CurrentCodexThread(conversationID)
	if ok {
		h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
	}
}

// chatWithAgent sends a message to an agent and returns the reply, with logging.
func (h *Handler) chatWithAgent(ctx context.Context, ag agent.Agent, userID, message string) (string, error) {
	return h.chatWithAgentWithProgress(ctx, ag, userID, message, nil)
}

// chatWithAgentWithProgress sends a message and optionally forwards incremental progress text.
func (h *Handler) chatWithAgentWithProgress(ctx context.Context, ag agent.Agent, userID, message string, onProgress func(delta string)) (string, error) {
	info := ag.Info()
	log.Printf("[handler] dispatching to agent (%s) for %s", info, userID)

	start := time.Now()
	var (
		reply string
		err   error
	)

	if streamAgent, ok := ag.(ProgressChatAgent); ok {
		reply, err = streamAgent.ChatWithProgress(ctx, userID, message, onProgress)
	} else {
		reply, err = ag.Chat(ctx, userID, message)
	}
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[handler] agent error (%s, elapsed=%s): %v", info, elapsed, err)
		return "", err
	}

	log.Printf("[handler] agent replied (%s, elapsed=%s): %q", info, elapsed, truncate(reply, 100))
	return reply, nil
}

// switchDefault switches the default agent. Starts it on demand if needed.
// The change is persisted to config file.
func (h *Handler) switchDefault(ctx context.Context, name string) string {
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to switch default to %q: %v", name, err)
		return fmt.Sprintf("Failed to switch to %q: %v", name, err)
	}

	h.mu.Lock()
	old := h.defaultName
	h.defaultName = name
	h.agents[name] = ag
	h.mu.Unlock()

	// Persist to config file
	if h.saveDefault != nil {
		if err := h.saveDefault(name); err != nil {
			log.Printf("[handler] failed to save default agent to config: %v", err)
		} else {
			log.Printf("[handler] saved default agent %q to config", name)
		}
	}

	info := ag.Info()
	log.Printf("[handler] switched default agent: %s -> %s (%s)", old, name, info)
	return fmt.Sprintf("switch to %s", name)
}

// resetDefaultSession resets the session for the given userID on the default agent.
func (h *Handler) resetDefaultSession(ctx context.Context, userID string) string {
	name, ag := h.getDefaultAgentWithName()
	if ag == nil {
		return "No agent running."
	}
	if isCodexAgent(name, ag.Info()) {
		return h.resetDefaultCodexSession(ctx, userID, name, ag)
	}
	if isClaudeAgent(name, ag.Info()) {
		return h.resetDefaultClaudeSession(ctx, userID, name, ag)
	}
	sessionID, err := ag.ResetSession(ctx, userID)
	if err != nil {
		log.Printf("[handler] reset session failed for %s: %v", userID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	if sessionID != "" {
		return wechatCommandText(fmt.Sprintf("已创建新的%s会话", name), sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

func (h *Handler) getDefaultAgentWithName() (string, agent.Agent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.defaultName == "" {
		return "", nil
	}
	return h.defaultName, h.agents[h.defaultName]
}

// resetDefaultCodexSession 重置当前微信用户正在使用的 Codex 工作空间会话。
func (h *Handler) resetDefaultCodexSession(ctx context.Context, userID string, name string, ag agent.Agent) string {
	workspaceRoot := h.codexWorkspaceRootForUser(userID, name, ag)
	conversationID := buildCodexConversationID(userID, name, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	sessionID, err := ag.ResetSession(ctx, conversationID)
	if err != nil {
		log.Printf("[handler] reset codex session failed for %s: %v", conversationID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	h.recordResetCodexThread(userID, name, workspaceRoot, sessionID)
	if sessionID != "" {
		return wechatCommandText(fmt.Sprintf("已创建新的%s会话", name), sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

// recordResetCodexThread 同步 /new 后的新 thread，避免下一条消息恢复旧工作空间 thread。
func (h *Handler) recordResetCodexThread(userID string, agentName string, workspaceRoot string, threadID string) {
	bindingKey := codexBindingKey(userID, agentName)
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	if strings.TrimSpace(threadID) == "" {
		h.ensureCodexSessions().setPendingNew(bindingKey, workspaceRoot)
		return
	}
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
}

func (h *Handler) resetDefaultClaudeSession(ctx context.Context, userID string, name string, ag agent.Agent) string {
	workspaceRoot := h.claudeWorkspaceRootForUser(userID, name, ag)
	conversationID := buildClaudeConversationID(userID, name, workspaceRoot)
	sessionID, err := ag.ResetSession(ctx, conversationID)
	if err != nil {
		log.Printf("[handler] reset claude session failed for %s: %v", conversationID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	h.recordResetClaudeSession(userID, name, workspaceRoot, sessionID)
	if sessionID != "" {
		return wechatCommandText(fmt.Sprintf("已创建新的%s会话", name), sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

func (h *Handler) recordResetClaudeSession(userID string, agentName string, workspaceRoot string, sessionID string) {
	bindingKey := claudeBindingKey(userID, agentName)
	h.ensureClaudeSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	if strings.TrimSpace(sessionID) == "" {
		h.ensureClaudeSessions().setPendingNew(bindingKey, workspaceRoot)
		return
	}
	h.ensureClaudeSessions().setSession(bindingKey, workspaceRoot, sessionID)
}

// handleCwd handles the /cwd command. It updates the working directory for all running agents.
func (h *Handler) handleCwd(trimmed string, userID ...string) string {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cwd"))
	if arg == "" {
		// No path provided — show current cwd of default agent
		ag := h.getDefaultAgent()
		if ag == nil {
			return "No agent running."
		}
		info := ag.Info()
		return wechatCommandText("cwd: (check agent config)", "agent: "+info.Name)
	}

	// Expand ~ to home directory
	if arg == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = home
		}
	} else if strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = filepath.Join(home, arg[2:])
		}
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Sprintf("Invalid path: %v", err)
	}

	// Verify directory exists
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Sprintf("Path not found: %s", absPath)
	}
	if !info.IsDir() {
		return fmt.Sprintf("Not a directory: %s", absPath)
	}

	// Update cwd on all running agents
	h.mu.RLock()
	agents := make(map[string]agent.Agent, len(h.agents))
	for name, ag := range h.agents {
		agents[name] = ag
	}
	h.mu.RUnlock()

	for name, ag := range agents {
		ag.SetCwd(absPath)
		log.Printf("[handler] updated cwd for agent %s: %s", name, absPath)
	}

	h.mu.Lock()
	if h.agentWorkDirs == nil {
		h.agentWorkDirs = make(map[string]string)
	}
	for name := range agents {
		h.agentWorkDirs[name] = absPath
	}
	h.mu.Unlock()
	h.recordActiveWorkspaceForUser(userID, agents, absPath)

	return fmt.Sprintf("cwd: %s", absPath)
}

func (h *Handler) recordActiveWorkspaceForUser(userIDs []string, agents map[string]agent.Agent, workspaceRoot string) {
	if len(userIDs) == 0 || strings.TrimSpace(userIDs[0]) == "" {
		return
	}
	for name, ag := range agents {
		if isCodexAgent(name, ag.Info()) {
			h.ensureCodexSessions().setActiveWorkspace(codexBindingKey(userIDs[0], name), workspaceRoot)
		}
		if isClaudeAgent(name, ag.Info()) {
			h.ensureClaudeSessions().setActiveWorkspace(claudeBindingKey(userIDs[0], name), workspaceRoot)
		}
	}
}

// buildStatus returns a short status string showing the current default agent.
func (h *Handler) buildStatus() string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.defaultName == "" {
		return "agent: none (echo mode)"
	}

	ag, ok := h.agents[h.defaultName]
	if !ok {
		return fmt.Sprintf("agent: %s (not started)", h.defaultName)
	}

	info := ag.Info()
	return wechatCommandText(
		"agent: "+h.defaultName,
		"type: "+info.Type,
		"model: "+agentStatusModelValue(info.Model),
	)
}

// agentStatusModelValue 用明确文案区分空模型配置和真实模型名。
func agentStatusModelValue(model string) string {
	if strings.TrimSpace(model) == "" {
		return "(Agent 默认)"
	}
	return model
}

func buildHelpText() string {
	return `WeClaw 帮助

常用：

/status 查看 WeClaw 运行态

/new 新建会话

/cwd <路径> 切换工作目录

Codex：

/cx status 查看 Codex 会话状态

/cx quota 查看 Codex 账号额度

/cx ls 查看列表

/cx <编号|..> 选择或返回

/cx cli 打开本地 CLI

/cx app 打开 Codex App

发送消息：

/codex <内容> 发给 Codex

/cc <内容> 发给 Claude

@cc @cx <内容> 同时发送

更多：

/cx help Codex 高级命令

/progress 查看进度模式`
}

// wechatCommandText 将内置命令回复转换为空行分隔，避免微信气泡折叠单换行。
func wechatCommandText(parts ...string) string {
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = normalizeCommandNewlines(part)
		for _, line := range strings.Split(part, "\n") {
			line = strings.TrimRight(line, " \t")
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n\n")
}

func normalizeCommandNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func isRemovedSwitchCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	return len(fields) > 0 && fields[0] == "/sw"
}

func isProgressCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	return len(fields) > 0 && fields[0] == "/progress"
}

func isCodexSessionCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || !isCodexSessionCommandToken(fields[0]) {
		return false
	}
	if isCodexShortSelectionToken(fields[1]) {
		return len(fields) == 2
	}
	switch fields[1] {
	case "whoami", "ls", "new", "switch", "cd", "pwd", "model", "quota", "cli", "attach", "detach", "app", "open-app", "status", "clean", "help":
		return true
	default:
		return false
	}
}

func isCodexSessionCommandToken(token string) bool {
	return token == "/codex" || token == "/cx"
}

func isCodexShortSelectionToken(token string) bool {
	if token == ".." {
		return true
	}
	_, ok := parseCodexListIndex(token)
	return ok
}

func (h *Handler) handleProgressCommand(trimmed string) string {
	return h.handleProgressCommandForPlatform(trimmed, "")
}

func (h *Handler) handleProgressCommandForPlatform(trimmed string, platformName platform.PlatformName) string {
	fields := strings.Fields(trimmed)
	if len(fields) == 1 {
		return wechatCommandText(
			"当前进度模式："+h.resolveProgressConfigForProgressCommand(platformName).Mode,
			"可用模式：off、typing、summary、verbose、stream、debug",
		)
	}
	if len(fields) != 2 {
		return "用法：/progress 或 /progress <off|typing|summary|verbose|stream|debug>"
	}

	mode := fields[1]
	if !isSupportedProgressMode(mode) {
		return wechatCommandText(
			"不支持的进度模式："+mode,
			"可用模式：off、typing、summary、verbose、stream、debug",
		)
	}

	cfg := h.resolveProgressConfigForProgressCommand(platformName)
	cfg.Mode = mode
	h.setProgressConfigForProgressCommand(platformName, cfg)
	return "已切换进度模式：" + mode
}

func (h *Handler) resolveProgressConfigForProgressCommand(platformName platform.PlatformName) config.ProgressConfig {
	if platformName == "" {
		return h.resolveProgressConfig("")
	}
	return h.resolveProgressConfigForPlatform(platformName, "")
}

func (h *Handler) setProgressConfigForProgressCommand(platformName platform.PlatformName, cfg config.ProgressConfig) {
	if platformName == "" {
		h.SetProgressConfig(cfg)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.platformProgressConfigs == nil {
		h.platformProgressConfigs = make(map[string]config.ProgressConfig)
	}
	h.platformProgressConfigs[string(platformName)] = cfg
}

func (h *Handler) handleCodexSessionCommand(ctx context.Context, userID string, trimmed string) string {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || fields[1] == "help" {
		return buildCodexSessionHelpText()
	}
	if fields[1] == "model" && isCodexModelStatusArgs(fields[2:]) {
		return h.renderCodexModelStatusFromConfig()
	}

	agentName, ag, err := h.getCodexSessionAgent(ctx)
	if err != nil {
		return err.Error()
	}
	workspaceRoot := h.codexWorkspaceRoot(agentName)
	bindingKey := codexBindingKey(userID, agentName)
	h.ensureCodexSessions().ensureWorkspace(bindingKey, workspaceRoot)
	h.syncCodexThreadFromAgent(userID, agentName, workspaceRoot, ag)

	if len(fields) == 2 && isCodexShortSelectionToken(fields[1]) {
		return h.handleCodexShortSelection(ctx, userID, agentName, workspaceRoot, ag, bindingKey, fields[1])
	}

	switch fields[1] {
	case "whoami":
		return h.renderCodexWhoami(bindingKey, workspaceRoot)
	case "ls":
		return h.renderCodexList(bindingKey)
	case "cd":
		if len(fields) != 3 {
			return "用法: /cx cd <编号|工作空间名|..>"
		}
		return h.handleCodexCd(codexWorkspaceCdRequest{
			Context:    ctx,
			UserID:     userID,
			BindingKey: bindingKey,
			AgentName:  agentName,
			Target:     fields[2],
			Agent:      ag,
		})
	case "pwd":
		return h.renderCodexPwd(bindingKey)
	case "status":
		if len(fields) != 2 {
			return "用法: /cx status"
		}
		return h.renderCodexStatus(userID, agentName, workspaceRoot, ag)
	case "quota":
		if len(fields) != 2 {
			return "用法: /cx quota"
		}
		return h.renderCodexQuota(ctx, ag)
	case "clean":
		if len(fields) != 2 {
			return "用法: /cx clean"
		}
		return h.handleCodexClean(bindingKey)
	case "app", "open-app":
		if len(fields) != 2 {
			return "用法: /cx app"
		}
		return h.handleCodexOpenApp(ctx, userID, agentName, workspaceRoot, ag)
	case "cli":
		if len(fields) != 2 {
			return "用法: /cx cli"
		}
		return h.handleCodexCLI(ctx, userID, agentName, workspaceRoot, ag)
	case "attach":
		if len(fields) == 3 && fields[2] == "app" {
			return h.handleCodexOpenApp(ctx, userID, agentName, workspaceRoot, ag)
		}
		if len(fields) != 2 {
			return "用法: /cx attach 或 /cx attach app"
		}
		return h.handleCodexAttach(ctx, userID, agentName, workspaceRoot, ag)
	case "detach":
		if len(fields) != 2 {
			return "用法: /cx detach"
		}
		return h.handleCodexDetach(ag)
	case "model":
		return h.handleCodexModelCommand(ctx, ag, fields[2:])
	case "new":
		return h.handleCodexNew(userID, agentName, workspaceRoot, ag)
	case "switch":
		if len(fields) != 3 {
			return "用法: /cx switch <编号|threadId>"
		}
		return h.handleCodexSwitch(ctx, userID, agentName, workspaceRoot, ag, fields[2])
	default:
		return buildCodexSessionHelpText()
	}
}

func (h *Handler) handleCodexShortSelection(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent, bindingKey string, target string) string {
	if target == ".." {
		return h.handleCodexCd(codexWorkspaceCdRequest{
			Context:    ctx,
			UserID:     userID,
			BindingKey: bindingKey,
			AgentName:  agentName,
			Target:     target,
			Agent:      ag,
		})
	}
	if _, browsing := h.codexBrowseWorkspace(bindingKey); browsing {
		return h.handleCodexSwitch(ctx, userID, agentName, workspaceRoot, ag, target)
	}
	return h.handleCodexCd(codexWorkspaceCdRequest{
		Context:    ctx,
		UserID:     userID,
		BindingKey: bindingKey,
		AgentName:  agentName,
		Target:     target,
		Agent:      ag,
	})
}

func (h *Handler) handleCodexClean(bindingKey string) string {
	removed := h.ensureCodexSessions().cleanMissingWorkspaces(bindingKey)
	if len(removed) == 0 {
		return "没有需要清理的 Codex 工作空间。"
	}
	if browsing, ok := h.codexBrowseWorkspace(bindingKey); ok && containsWorkspaceRoot(removed, browsing) {
		h.clearCodexBrowseWorkspace(bindingKey)
	}
	names := make([]string, 0, len(removed))
	for _, root := range removed {
		names = append(names, shortCodexWorkspaceName(root))
	}
	return wechatCommandText(
		fmt.Sprintf("已清理 Codex 工作空间：%d 个", len(removed)),
		"已移除："+strings.Join(names, "、"),
		"未删除 Codex App 历史文件。",
	)
}

func containsWorkspaceRoot(roots []string, target string) bool {
	target = normalizeCodexWorkspaceRoot(target)
	for _, root := range roots {
		if normalizeCodexWorkspaceRoot(root) == target {
			return true
		}
	}
	return false
}

// handleCodexOpenApp 打开当前工作区的 Codex App，并尽量回显当前 thread 便于用户确认。
func (h *Handler) handleCodexOpenApp(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	workspaceRoot = h.codexWorkspaceRootForUser(userID, agentName, ag)
	h.syncCodexThreadFromAgent(userID, agentName, workspaceRoot, ag)
	opener := h.resolveCodexAppOpener()
	command := strings.TrimSpace(ag.Info().Command)
	if command == "" {
		return "当前 Codex Agent 未配置 command，无法打开 Codex App。"
	}
	if err := opener(ctx, command, workspaceRoot); err != nil {
		return wechatCommandText(
			fmt.Sprintf("打开 Codex App 失败: %v", err),
			"可发送 /cx cli 使用 Codex CLI 接手当前 thread。",
		)
	}
	bindingKey := codexBindingKey(userID, agentName)
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	h.setCodexBrowseWorkspace(bindingKey, workspaceRoot)
	h.recordCodexLocalEntry(bindingKey, workspaceRoot, codexLocalEntryApp)
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	return wechatCommandText(
		"已打开 Codex App。",
		"工作空间: "+workspaceRoot,
		"thread: "+renderCodexThreadLabel(threadID, pending),
	)
}

func (h *Handler) resolveCodexAppOpener() CodexAppOpener {
	h.mu.RLock()
	opener := h.codexAppOpener
	h.mu.RUnlock()
	if opener == nil {
		return defaultCodexAppOpener
	}
	return opener
}

// defaultCodexAppOpener 使用当前 Codex 命令打开桌面 App 的工作区入口。
func defaultCodexAppOpener(ctx context.Context, command string, workspaceRoot string) error {
	cmd := exec.CommandContext(ctx, command, "app", workspaceRoot)
	cmd.Dir = workspaceRoot
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// handleCodexAttach 将当前 Codex 会话打开到本地可见端；remote-first Agent 使用 resume。
func (h *Handler) handleCodexAttach(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	visibleAg, ok := ag.(agent.VisibleCompanionAgent)
	if !ok {
		return h.handleCodexAttachResume(ctx, userID, agentName, workspaceRoot, ag)
	}
	if err := visibleAg.OpenVisibleCompanion(ctx); err != nil {
		return fmt.Sprintf("打开 Codex 本地可见端失败: %v", err)
	}
	return "已打开 Codex 本地可见端。"
}

// handleCodexCLI 将当前微信 Codex thread 恢复到本地 CLI，便于电脑端接手。
func (h *Handler) handleCodexCLI(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.openCodexThreadInCLI(ctx, userID, agentName, workspaceRoot, ag, codexCLIOpenText{
		unsupported:      "当前 Codex Agent 不支持 cli。",
		missingCommand:   "当前 Codex Agent 未配置 command，无法打开 Codex CLI。",
		openFailedPrefix: "打开 Codex CLI 失败",
		successTitle:     "已打开 Codex CLI。",
	})
}

func (h *Handler) handleCodexAttachResume(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.openCodexThreadInCLI(ctx, userID, agentName, workspaceRoot, ag, codexCLIOpenText{
		unsupported:      "当前 Codex Agent 不支持 attach。",
		missingCommand:   "当前 Codex Agent 未配置 command，无法打开本地可见端。",
		openFailedPrefix: "打开 Codex 本地可见端失败",
		successTitle:     "已打开 Codex 本地可见端。",
	})
}

type codexCLIOpenText struct {
	unsupported      string
	missingCommand   string
	openFailedPrefix string
	successTitle     string
}

type codexLocalEntryState struct {
	CLIOpened bool
	AppOpened bool
}

const (
	codexLocalEntryCLI = "cli"
	codexLocalEntryApp = "app"
)

func (h *Handler) openCodexThreadInCLI(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent, text codexCLIOpenText) string {
	if _, ok := ag.(agent.CodexThreadAgent); !ok {
		return text.unsupported
	}
	workspaceRoot = h.codexWorkspaceRootForUser(userID, agentName, ag)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = h.codexWorkspaceRoot(agentName)
	}
	h.syncCodexThreadFromAgent(userID, agentName, workspaceRoot, ag)
	bindingKey := codexBindingKey(userID, agentName)
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	if pending || strings.TrimSpace(threadID) == "" {
		return "当前还没有可接手的 Codex thread，请先通过微信发送一条 Codex 任务。"
	}
	command := strings.TrimSpace(ag.Info().Command)
	if command == "" {
		return text.missingCommand
	}
	if err := h.resolveCodexCLIResumeOpener()(ctx, command, workspaceRoot, threadID); err != nil {
		return fmt.Sprintf("%s: %v", text.openFailedPrefix, err)
	}
	h.recordCodexLocalEntry(bindingKey, workspaceRoot, codexLocalEntryCLI)
	return wechatCommandText(
		text.successTitle,
		"工作空间: "+workspaceRoot,
		"thread: "+threadID,
	)
}

func (h *Handler) recordCodexLocalEntry(bindingKey string, workspaceRoot string, entryType string) {
	key := codexLocalEntryKey(bindingKey, workspaceRoot)
	if key == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.codexLocalEntries == nil {
		h.codexLocalEntries = make(map[string]codexLocalEntryState)
	}
	state := h.codexLocalEntries[key]
	switch entryType {
	case codexLocalEntryCLI:
		state.CLIOpened = true
	case codexLocalEntryApp:
		state.AppOpened = true
	default:
		return
	}
	h.codexLocalEntries[key] = state
}

func (h *Handler) codexLocalEntry(bindingKey string, workspaceRoot string) codexLocalEntryState {
	key := codexLocalEntryKey(bindingKey, workspaceRoot)
	if key == "" {
		return codexLocalEntryState{}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.codexLocalEntries[key]
}

func codexLocalEntryKey(bindingKey string, workspaceRoot string) string {
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	if strings.TrimSpace(bindingKey) == "" || workspaceRoot == "" {
		return ""
	}
	return bindingKey + "\x00" + workspaceRoot
}

func (h *Handler) resolveCodexCLIResumeOpener() CodexCLIResumeOpener {
	h.mu.RLock()
	opener := h.codexCLIResumeOpener
	h.mu.RUnlock()
	if opener == nil {
		return defaultCodexCLIResumeOpener
	}
	return opener
}

// defaultCodexCLIResumeOpener 在 Terminal 中恢复当前 Codex thread，便于本地接手。
func defaultCodexCLIResumeOpener(ctx context.Context, command string, workspaceRoot string, threadID string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("当前平台 %s 暂不支持自动打开可见终端", runtime.GOOS)
	}
	parts := []string{
		shellQuoteForTerminal(command),
		"resume",
		shellQuoteForTerminal(threadID),
		"--cd",
		shellQuoteForTerminal(workspaceRoot),
	}
	script := "tell application \"Terminal\" to do script " + appleScriptQuoteForTerminal(strings.Join(parts, " "))
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func shellQuoteForTerminal(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func appleScriptQuoteForTerminal(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

// handleCodexDetach 仅断开本地可见 Companion，后台 endpoint 继续服务微信 remote。
func (h *Handler) handleCodexDetach(ag agent.Agent) string {
	visibleAg, ok := ag.(agent.VisibleCompanionAgent)
	if !ok {
		return "当前 Codex Agent 不支持 detach。"
	}
	if !visibleAg.DetachVisibleCompanion() {
		return "当前没有已连接的 Codex 本地可见端。"
	}
	return "已断开 Codex 本地可见端。"
}

func (h *Handler) handleCodexNew(userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	if codexAg, ok := ag.(agent.CodexThreadAgent); ok {
		codexAg.ClearCodexThread(conversationID)
	}
	bindingKey := codexBindingKey(userID, agentName)
	h.ensureCodexSessions().setPendingNew(bindingKey, workspaceRoot)
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	return wechatCommandText("已切换到新会话。", "workspace: "+workspaceRoot)
}

func (h *Handler) handleCodexSwitch(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent, target string) string {
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		return "当前 Codex Agent 不支持 thread 切换。"
	}
	bindingKey := codexBindingKey(userID, agentName)
	workspaceRoot, threadID, err := h.resolveCodexSwitchTarget(bindingKey, agentName, workspaceRoot, target, ag)
	if err != nil {
		return err.Error()
	}
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	if err := codexAg.UseCodexThread(ctx, conversationID, threadID); err != nil {
		return renderCodexSwitchFailure(err)
	}
	h.switchCodexWorkspace(agentName, workspaceRoot, ag)
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	return wechatCommandText("已切换会话。", "工作空间: "+shortCodexWorkspaceName(workspaceRoot))
}

func (h *Handler) resolveCodexSwitchTarget(bindingKey string, agentName string, workspaceRoot string, target string, ag agent.Agent) (string, string, error) {
	target = strings.TrimSpace(target)
	if index, ok := parseCodexListIndex(target); ok {
		if view, ok := h.resolveCodexSessionByIndex(bindingKey, index); ok {
			return h.resolveCodexSessionView(agentName, view, ag)
		}
		if _, browsing := h.codexBrowseWorkspace(bindingKey); browsing {
			return "", "", fmt.Errorf("会话编号不存在，请先发送 /cx ls 查看当前工作空间会话。")
		}
		views := h.codexSwitchTargets(bindingKey)
		if index < 0 || index >= len(views) {
			return "", "", fmt.Errorf("编号不存在，请先发送 /codex ls 查看可切换会话。")
		}
		return h.resolveCodexSessionView(agentName, views[index], ag)
	}
	threadID := target
	workspaceRoot = h.resolveCodexSwitchWorkspace(bindingKey, agentName, workspaceRoot, threadID, ag)
	return workspaceRoot, threadID, nil
}

func (h *Handler) resolveCodexSessionView(agentName string, view codexWorkspaceView, ag agent.Agent) (string, string, error) {
	threadID := strings.TrimSpace(view.ThreadID)
	if threadID == "" || view.PendingNewThread {
		return "", "", fmt.Errorf("该编号当前没有可切换的会话。")
	}
	return normalizeCodexWorkspaceRoot(view.WorkspaceRoot), threadID, nil
}

func parseCodexListIndex(value string) (int, bool) {
	if strings.TrimSpace(value) == "" {
		return 0, false
	}
	index, err := strconv.Atoi(value)
	return index, err == nil
}

func (h *Handler) resolveCodexSwitchWorkspace(bindingKey string, agentName string, fallbackWorkspace string, threadID string, ag agent.Agent) string {
	workspaceRoot, ok := h.ensureCodexSessions().findWorkspaceByThread(bindingKey, threadID)
	if ok {
		return normalizeCodexWorkspaceRoot(workspaceRoot)
	}
	if localWorkspace, ok := h.findLocalCodexWorkspaceByThread(threadID); ok {
		return normalizeCodexWorkspaceRoot(localWorkspace)
	}
	return normalizeCodexWorkspaceRoot(fallbackWorkspace)
}

func renderCodexSwitchFailure(err error) string {
	if isCodexThreadStoreReadError(err) {
		return wechatCommandText(
			"切换会话失败。",
			"该 Codex 会话当前无法被微信接手。",
			"可发送 /cx app 在 Codex App 中打开当前工作空间，或发送 /cx new 创建微信侧新会话。",
		)
	}
	return fmt.Sprintf("切换线程失败: %v", err)
}

func isCodexThreadStoreReadError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "thread-store internal error") ||
		strings.Contains(text, "failed to read thread") ||
		strings.Contains(text, "does not start with session metadata")
}

func (h *Handler) switchCodexWorkspace(agentName string, workspaceRoot string, ag agent.Agent) string {
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	ag.SetCwd(workspaceRoot)

	h.mu.Lock()
	if h.agentWorkDirs == nil {
		h.agentWorkDirs = make(map[string]string)
	}
	h.agentWorkDirs[agentName] = workspaceRoot
	h.mu.Unlock()
	log.Printf("[handler] switched codex workspace for agent %s: %s", agentName, workspaceRoot)
	return workspaceRoot
}

func (h *Handler) getCodexSessionAgent(ctx context.Context) (string, agent.Agent, error) {
	agentName, ok := h.codexAgentName()
	if !ok {
		return "", nil, fmt.Errorf("当前没有配置 Codex Agent。")
	}
	ag, err := h.getAgent(ctx, agentName)
	if err != nil {
		return "", nil, fmt.Errorf("Codex Agent 不可用: %v", err)
	}
	return agentName, ag, nil
}

func (h *Handler) codexAgentName() (string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if ag, ok := h.agents["codex"]; ok && isCodexAgent("codex", ag.Info()) {
		return "codex", true
	}
	if h.defaultName != "" {
		if ag, ok := h.agents[h.defaultName]; ok && isCodexAgent(h.defaultName, ag.Info()) {
			return h.defaultName, true
		}
	}
	for _, meta := range h.agentMetas {
		if meta.Name == "codex" || isCodexAgent(meta.Name, agent.AgentInfo{Name: meta.Name, Type: meta.Type, Command: meta.Command}) {
			return meta.Name, true
		}
	}
	return "", false
}

func (h *Handler) codexWorkspaceRoot(agentName string) string {
	h.mu.RLock()
	workspaceRoot := h.agentWorkDirs[agentName]
	h.mu.RUnlock()
	if workspaceRoot == "" {
		workspaceRoot = defaultAttachmentWorkspace()
	}
	return normalizeCodexWorkspaceRoot(workspaceRoot)
}

func (h *Handler) codexWorkspaceRootForUser(userID string, agentName string, ag agent.Agent) string {
	bindingKey := codexBindingKey(userID, agentName)
	workspaceRoot, ok := h.ensureCodexSessions().getActiveWorkspace(bindingKey)
	if !ok {
		return h.codexWorkspaceRoot(agentName)
	}
	ag.SetCwd(workspaceRoot)
	h.mu.Lock()
	if h.agentWorkDirs == nil {
		h.agentWorkDirs = make(map[string]string)
	}
	h.agentWorkDirs[agentName] = workspaceRoot
	h.mu.Unlock()
	return workspaceRoot
}

func (h *Handler) renderCodexWhoami(bindingKey string, workspaceRoot string) string {
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	return wechatCommandText("workspace: "+workspaceRoot, "thread: "+renderCodexThreadLabel(threadID, pending))
}

func (h *Handler) renderCodexStatus(userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	workspaceRoot = h.codexWorkspaceRootForUser(userID, agentName, ag)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = h.codexWorkspaceRoot(agentName)
	}
	h.syncCodexThreadFromAgent(userID, agentName, workspaceRoot, ag)

	bindingKey := codexBindingKey(userID, agentName)
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	localEntry := h.codexLocalEntry(bindingKey, workspaceRoot)
	return wechatCommandText(
		"Codex 状态:",
		"工作空间: "+workspaceRoot,
		"thread: "+renderCodexThreadLabel(threadID, pending),
		"remote: 已配置 ("+ag.Info().Type+")",
		"本地入口:",
		"CLI: "+renderCodexLocalEntry(localEntry.CLIOpened),
		"App: "+renderCodexLocalEntry(localEntry.AppOpened),
		"说明: 本地入口只记录最近打开动作，不实时检测手动关闭。",
	)
}

func renderCodexLocalEntry(opened bool) string {
	if opened {
		return "已打开过"
	}
	return "未打开过"
}

func (h *Handler) renderCodexList(bindingKey string) string {
	if workspaceRoot, ok := h.codexBrowseWorkspace(bindingKey); ok {
		return h.renderCodexSessionList(bindingKey, workspaceRoot)
	}
	return h.renderCodexWorkspaceList(bindingKey)
}

func renderCodexThreadLabel(threadID string, pending bool) string {
	if pending {
		return "(new draft)"
	}
	if strings.TrimSpace(threadID) == "" {
		return "(none)"
	}
	return threadID
}

func buildCodexSessionHelpText() string {
	return wechatCommandText(
		"Codex 会话命令:",
		"/cx ls 查看工作空间或当前工作空间会话",
		"/cx <编号|..> 选择当前列表项或返回上一级",
		"/cx cd <编号|工作空间名|..> 进入工作空间或返回工作空间列表",
		"/cx switch <编号> 切换当前工作空间会话",
		"/cx new 新建当前工作空间会话",
		"/cx pwd 查看当前工作空间",
		"/cx cli 打开本地 CLI 接手当前 thread",
		"/cx app 打开 Codex App 到当前工作空间",
		"/cx status 查看 remote、thread 和本地入口状态",
		"/cx quota 查看 Codex 账号额度",
		"/cx clean 清理已不存在的 WeClaw 工作空间记录",
		"/cx attach app 兼容写法，等同 /cx app",
		"/cx detach 断开已连接的本地 Companion",
		"/cx model status 查看 Codex 模型状态",
		"/cx model ls 查看可用 Codex 模型",
		"/codex 可作为 /cx 的兼容写法",
	)
}

func isSupportedProgressMode(mode string) bool {
	switch mode {
	case progressModeOff, progressModeTyping, progressModeSummary, progressModeVerbose, progressModeStream, progressModeDebug:
		return true
	default:
		return false
	}
}

func isCodexAgent(name string, info agent.AgentInfo) bool {
	if strings.EqualFold(name, "codex") || strings.EqualFold(info.Name, "codex") {
		return true
	}
	command := strings.ToLower(filepath.Base(info.Command))
	return strings.Contains(command, "codex")
}

type savedIncomingFile struct {
	name string
	path string
}

func firstAttachment(attachments []platform.Attachment, kind platform.AttachmentKind) (platform.Attachment, bool) {
	for _, attachment := range attachments {
		if attachment.Kind == kind {
			return attachment, true
		}
	}
	return platform.Attachment{}, false
}

func (h *Handler) handleFileAttachment(ctx context.Context, userID string, reply platform.Replier, file platform.Attachment, text string) (string, bool) {
	saved, err := h.saveIncomingAttachment(ctx, file)
	if err != nil {
		log.Printf("[handler] failed to save incoming file from %s: %v", userID, err)
		sendPlatformText(ctx, reply, userID, fmt.Sprintf("文件保存失败：%v", err))
		return "", false
	}
	log.Printf("[handler] saved incoming file from %s: %s", userID, saved.path)
	return buildFileAgentMessage(text, saved), true
}

func (h *Handler) handleImageAttachment(ctx context.Context, userID string, reply platform.Replier, image platform.Attachment, text string) (string, bool) {
	saved, err := h.saveIncomingAttachment(ctx, image)
	if err != nil {
		log.Printf("[handler] failed to save incoming image from %s: %v", userID, err)
		sendPlatformText(ctx, reply, userID, fmt.Sprintf("图片保存失败：%v", err))
		return "", false
	}
	log.Printf("[handler] saved incoming image from %s: %s", userID, saved.path)
	return buildImageAgentMessage(text, saved), true
}

func (h *Handler) saveIncomingAttachment(ctx context.Context, file platform.Attachment) (savedIncomingFile, error) {
	data, err := h.readAttachmentData(ctx, file)
	if err != nil {
		return savedIncomingFile{}, err
	}
	fileName := safeIncomingFileName(file.FileName)
	dir := h.incomingFileDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return savedIncomingFile{}, fmt.Errorf("创建保存目录失败：%w", err)
	}
	path := filepath.Join(dir, time.Now().Format("20060102-150405")+"-"+fileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return savedIncomingFile{}, fmt.Errorf("写入文件失败：%w", err)
	}
	return savedIncomingFile{name: fileName, path: path}, nil
}

func (h *Handler) readAttachmentData(ctx context.Context, attachment platform.Attachment) ([]byte, error) {
	if attachment.Path != "" {
		return os.ReadFile(attachment.Path)
	}
	encryptQueryParam := attachment.Metadata["encrypt_query_param"]
	aesKey := attachment.Metadata["aes_key"]
	if encryptQueryParam == "" || aesKey == "" {
		return nil, fmt.Errorf("文件缺少下载信息")
	}
	downloader := h.cdnDownloader
	if downloader == nil {
		downloader = wechat.DownloadFileFromCDN
	}
	return downloader(ctx, encryptQueryParam, aesKey)
}

func (h *Handler) incomingFileDir() string {
	if strings.TrimSpace(h.saveDir) != "" {
		return h.saveDir
	}
	return defaultAttachmentWorkspace()
}

func safeIncomingFileName(fileName string) string {
	fileName = filepath.Base(strings.TrimSpace(fileName))
	if fileName == "." || fileName == string(filepath.Separator) || fileName == "" {
		return "wechat-file"
	}
	return fileName
}

func buildFileAgentMessage(userText string, file savedIncomingFile) string {
	userText = strings.TrimSpace(userText)
	fileInfo := "用户发送了一个文件，请查看并分析：\n文件名：" + file.name + "\n本地路径：" + file.path
	if userText == "" {
		return fileInfo
	}
	return userText + "\n\n" + fileInfo
}

func buildImageAgentMessage(userText string, file savedIncomingFile) string {
	userText = strings.TrimSpace(userText)
	imageInfo := "用户发送了一张图片，请查看并分析：\n文件名：" + file.name + "\n本地路径：" + file.path
	if userText == "" {
		return imageInfo
	}
	return userText + "\n\n" + imageInfo
}

func (h *Handler) handleImageAttachmentSave(ctx context.Context, userID string, reply platform.Replier, img platform.Attachment) {
	log.Printf("[handler] received image from %s, saving to %s", userID, h.saveDir)
	var data []byte
	var err error
	if img.SourceID != "" {
		data, _, err = downloadFile(ctx, img.SourceID)
	} else {
		data, err = h.readAttachmentData(ctx, img)
	}
	if err != nil {
		log.Printf("[handler] failed to download image from %s: %v", userID, err)
		sendPlatformText(ctx, reply, userID, fmt.Sprintf("Failed to save image: %v", err))
		return
	}
	ext := detectImageExt(data)
	filePath := filepath.Join(h.saveDir, time.Now().Format("20060102-150405")+ext)
	if err := os.MkdirAll(h.saveDir, 0o755); err != nil {
		log.Printf("[handler] failed to create save dir: %v", err)
		return
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		log.Printf("[handler] failed to write image: %v", err)
		sendPlatformText(ctx, reply, userID, fmt.Sprintf("Failed to save image: %v", err))
		return
	}
	sidecarPath := filePath + ".sidecar.md"
	sidecarContent := fmt.Sprintf("---\nid: %s\n---\n", uuid.New().String())
	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o644); err != nil {
		log.Printf("[handler] failed to write sidecar: %v", err)
	}
	sendPlatformText(ctx, reply, userID, fmt.Sprintf("Saved image: %s", filePath))
}

func detectImageExt(data []byte) string {
	if len(data) < 4 {
		return ".bin"
	}
	// PNG: 89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return ".png"
	}
	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return ".jpg"
	}
	// GIF: 47 49 46
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return ".gif"
	}
	// WebP: 52 49 46 46 ... 57 45 42 50
	if len(data) >= 12 && data[0] == 0x52 && data[1] == 0x49 && data[8] == 0x57 && data[9] == 0x45 {
		return ".webp"
	}
	// BMP: 42 4D
	if data[0] == 0x42 && data[1] == 0x4D {
		return ".bmp"
	}
	return ".jpg" // default to jpg for WeChat images
}
