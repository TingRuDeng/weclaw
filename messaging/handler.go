package messaging

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/google/uuid"
)

// AgentFactory creates an agent by config name. Returns nil if the name is unknown.
type AgentFactory func(ctx context.Context, name string) agent.Agent

// SaveDefaultFunc persists the default agent name to config file.
type SaveDefaultFunc func(name string) error

// SwitchCommandRunner 用于执行外部 codex 切换脚本。
type SwitchCommandRunner func(ctx context.Context, scriptPath string, args ...string) (string, error)

// CDNDownloader 用于下载微信 CDN 中的入站文件，便于测试注入。
type CDNDownloader func(ctx context.Context, encryptQueryParam string, aesKey string) ([]byte, error)

// ProgressChatAgent 支持在聊天过程中输出增量内容。
type ProgressChatAgent interface {
	ChatWithProgress(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error)
}

// StoppableAgent 支持显式停止后台进程，账号切换后必须释放旧 Codex 进程。
type StoppableAgent interface {
	Stop()
}

// AgentMeta holds static config info about an agent (for /status display).
type AgentMeta struct {
	Name    string
	Type    string // "acp", "cli", "http"
	Command string // binary path or endpoint
	Model   string
}

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	mu                   sync.RWMutex
	defaultName          string
	agents               map[string]agent.Agent // name -> running agent
	agentMetas           []AgentMeta            // all configured agents (for /status)
	agentWorkDirs        map[string]string      // agent name -> configured/runtime cwd
	customAliases        map[string]string      // custom alias -> agent name (from config)
	factory              AgentFactory
	saveDefault          SaveDefaultFunc
	contextTokens        sync.Map // map[userID]contextToken
	saveDir              string   // directory to save images/files to
	seenMsgs             sync.Map // map[int64]time.Time — dedup by message_id
	switchScript         string
	switchRunner         SwitchCommandRunner
	cdnDownloader        CDNDownloader
	progressConfig       config.ProgressConfig
	agentProgressConfigs map[string]config.ProgressConfig
	seenTextMsgs         sync.Map // map[string]time.Time — MessageID 为 0 时按文本去重
	codexSessions        *codexSessionStore
	taskLocksMu          sync.Mutex
	taskLocks            map[string]*sync.Mutex
	activeTasksMu        sync.Mutex
	activeTasks          map[string]*activeAgentTask
	codexLocalSessionDir string
}

const (
	switchScriptEnvVar      = "WECLAW_CODEX_SWITCH_SCRIPT"
	switchScriptDefaultPath = "/Volumes/Data/code/MyCode/cc-switch/codex-switch.sh"
	switchCommandTimeout    = 30 * time.Second
	switchCommandUsage      = "用法: /sw ls | /sw current | /sw reload | /sw <编号|ID>"
)

var ansiEscapePattern = regexp.MustCompile(`\x1B\[[0-?]*[ -/]*[@-~]`)

type activeAgentTask struct {
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
	detached       bool
	pendingMessage string
}

// NewHandler creates a new message handler.
func NewHandler(factory AgentFactory, saveDefault SaveDefaultFunc) *Handler {
	return &Handler{
		agents:               make(map[string]agent.Agent),
		agentWorkDirs:        make(map[string]string),
		factory:              factory,
		saveDefault:          saveDefault,
		switchScript:         resolveSwitchScriptPath(),
		switchRunner:         defaultSwitchCommandRunner,
		cdnDownloader:        DownloadFileFromCDN,
		progressConfig:       config.DefaultProgressConfig(),
		agentProgressConfigs: make(map[string]config.ProgressConfig),
		codexSessions:        newCodexSessionStore(),
		taskLocks:            make(map[string]*sync.Mutex),
		activeTasks:          make(map[string]*activeAgentTask),
		codexLocalSessionDir: defaultCodexLocalSessionDir(),
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

func (h *Handler) isDuplicateTextMessage(msg ilink.WeixinMessage, text string) bool {
	key := buildTextDedupKey(msg.FromUserID, msg.ContextToken, text)
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

func (h *Handler) resolveProgressConfig(agentName string) config.ProgressConfig {
	h.mu.RLock()
	global := h.progressConfig
	override, ok := h.agentProgressConfigs[agentName]
	h.mu.RUnlock()
	if global.Mode == "" {
		global = config.DefaultProgressConfig()
	}
	if !ok {
		return global
	}
	return config.NormalizeProgressConfig(global, &override)
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

// SetDefaultAgent sets the default agent (already started).
func (h *Handler) SetDefaultAgent(name string, ag agent.Agent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultName = name
	h.agents[name] = ag
	log.Printf("[handler] default agent ready: %s (%s)", name, ag.Info())
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

// HandleMessage processes a single incoming message.
func (h *Handler) HandleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	// Only process user messages that are finished
	if msg.MessageType != ilink.MessageTypeUser {
		return
	}
	if msg.MessageState != ilink.MessageStateFinish {
		return
	}

	// Deduplicate by message_id to avoid processing the same message multiple times
	// (voice messages may trigger multiple finish-state updates)
	if msg.MessageID != 0 {
		if _, loaded := h.seenMsgs.LoadOrStore(msg.MessageID, time.Now()); loaded {
			return
		}
		// Clean up old entries periodically (fire-and-forget)
		go h.cleanSeenMsgs(h.duplicateTTL())
	}

	// Extract text from item list (text message or voice transcription)
	text := extractText(msg)
	if text == "" {
		if voiceText := extractVoiceText(msg); voiceText != "" {
			text = voiceText
			log.Printf("[handler] voice transcription from %s: %q", msg.FromUserID, truncate(text, 80))
		}
	}
	if file := extractFile(msg); file != nil {
		fileText, ok := h.handleFileInput(ctx, client, msg, file, text)
		if !ok {
			return
		}
		text = fileText
	}
	if text == "" {
		// Check for image message
		if img := extractImage(msg); img != nil && h.saveDir != "" {
			h.handleImageSave(ctx, client, msg, img)
			return
		}
		log.Printf("[handler] received non-text message from %s, skipping", msg.FromUserID)
		return
	}
	if msg.MessageID == 0 && h.isDuplicateTextMessage(msg, text) {
		_ = SendTextReply(ctx, client, msg.FromUserID, duplicateTaskReply(), msg.ContextToken, NewClientID())
		return
	}

	log.Printf("[handler] received from %s: %q", msg.FromUserID, truncate(text, 80))

	// Store context token for this user
	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)

	// Generate a clientID for this reply (used to correlate typing → finish)
	clientID := NewClientID()

	// Intercept URLs: save to Linkhoard directly without AI agent
	trimmed := strings.TrimSpace(text)
	if h.saveDir != "" && IsURL(trimmed) {
		rawURL := ExtractURL(trimmed)
		if rawURL != "" {
			log.Printf("[handler] saving URL to linkhoard: %s", rawURL)
			title, err := SaveLinkToLinkhoard(ctx, h.saveDir, rawURL)
			var reply string
			if err != nil {
				log.Printf("[handler] link save failed: %v", err)
				reply = fmt.Sprintf("保存失败: %v", err)
			} else {
				reply = fmt.Sprintf("已保存: %s", title)
			}
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
			return
		}
	}

	// Built-in commands (no typing needed)
	if trimmed == "/info" {
		reply := h.buildStatus()
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if trimmed == "/help" {
		reply := buildHelpText()
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if trimmed == "/new" || trimmed == "/clear" {
		reply := h.resetDefaultSession(ctx, msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if isSwitchCommand(trimmed) {
		reply := h.handleSwitchCommand(ctx, trimmed)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if isProgressCommand(trimmed) {
		reply := h.handleProgressCommand(trimmed)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if isCodexSessionCommand(trimmed) {
		reply := h.handleCodexSessionCommand(ctx, msg.FromUserID, trimmed)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if trimmed == "/guide" {
		h.handleGuideCommand(ctx, client, msg, clientID)
		return
	} else if trimmed == "/cancel" {
		reply := h.handleCancelPendingGuide(ctx, msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if strings.HasPrefix(trimmed, "/cwd") {
		reply := h.handleCwd(trimmed, msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	}

	// Route: "/agentname message" or "@agent1 @agent2 message" -> specific agent(s)
	agentNames, message := h.parseCommand(text)

	// No command prefix -> send to default agent
	if len(agentNames) == 0 {
		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}

	// No message -> switch default agent (only first name)
	if message == "" {
		if len(agentNames) == 1 && h.isKnownAgent(agentNames[0]) {
			reply := h.switchDefault(ctx, agentNames[0])
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
		} else if len(agentNames) == 1 && !h.isKnownAgent(agentNames[0]) {
			// Unknown agent -> forward to default
			h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		} else {
			reply := "Usage: specify one agent to switch, or add a message to broadcast"
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
		}
		return
	}

	// Filter to known agents; if single unknown agent -> forward to default
	var knownNames []string
	for _, name := range agentNames {
		if h.isKnownAgent(name) {
			knownNames = append(knownNames, name)
		}
	}
	if len(knownNames) == 0 {
		// No known agents -> forward entire text to default agent
		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}

	if len(knownNames) == 1 {
		// Single agent
		h.sendToNamedAgent(ctx, client, msg, knownNames[0], message, clientID)
	} else {
		// Multi-agent broadcast: parallel dispatch, send replies as they arrive
		h.broadcastToAgents(ctx, client, msg, knownNames, message)
	}
}

func (h *Handler) agentExecutionKey(userID string, agentName string, ag agent.Agent) string {
	info := ag.Info()
	if isCodexAgent(agentName, info) {
		workspaceRoot := h.codexWorkspaceRootForUser(userID, agentName, ag)
		return buildCodexConversationID(userID, agentName, workspaceRoot)
	}
	return strings.Join([]string{"agent", strings.TrimSpace(userID), strings.TrimSpace(agentName)}, "\x00")
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

func (t *activeAgentTask) shouldSendFinal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.detached
}

func runningCodexGuidePrompt() string {
	return "Codex 正在处理上一条任务。\n\n回复 /guide 将此消息作为引导对话发送给 Codex。\n回复 /cancel 撤回该消息。"
}

func (h *Handler) handleGuideCommand(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, clientID string) {
	name, _, key, err := h.codexGuideTarget(ctx, msg.FromUserID)
	if err != nil {
		_ = SendTextReply(ctx, client, msg.FromUserID, err.Error(), msg.ContextToken, clientID)
		return
	}
	message, task, ok := h.detachPendingGuide(key)
	if !ok {
		_ = SendTextReply(ctx, client, msg.FromUserID, "当前没有可发送的引导对话。", msg.ContextToken, clientID)
		return
	}
	if !waitForActiveTask(ctx, task) {
		return
	}
	h.sendToNamedAgent(ctx, client, msg, name, message, clientID)
}

func (h *Handler) handleCancelPendingGuide(ctx context.Context, userID string) string {
	_, _, key, err := h.codexGuideTarget(ctx, userID)
	if err != nil {
		return err.Error()
	}
	if !h.clearPendingGuide(key) {
		return "当前没有可撤回的消息。"
	}
	return "已撤回该消息。"
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
func (h *Handler) sendToDefaultAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text, clientID string) {
	h.mu.RLock()
	defaultName := h.defaultName
	h.mu.RUnlock()

	ag := h.getDefaultAgent()
	var reply string
	if ag != nil {
		replyCtx := ctx
		progressCfg := h.resolveProgressConfig(defaultName)
		agentCtx, cancelTaskTimeout := contextWithTaskTimeout(ctx, progressCfg)
		defer cancelTaskTimeout()

		executionKey := h.agentExecutionKey(msg.FromUserID, defaultName, ag)
		var activeTask *activeAgentTask
		if isCodexAgent(defaultName, ag.Info()) {
			task, taskCtx, started := h.beginActiveTask(agentCtx, executionKey)
			if !started {
				h.storePendingGuide(executionKey, text)
				_ = SendTextReply(ctx, client, msg.FromUserID, runningCodexGuidePrompt(), msg.ContextToken, clientID)
				return
			}
			activeTask = task
			defer h.finishActiveTask(executionKey, task)
			agentCtx = taskCtx
		}
		unlock := h.lockAgentExecution(executionKey)
		defer unlock()

		onProgress, stopProgress := h.startProgressSession(agentCtx, client, msg.FromUserID, msg.ContextToken, "", text, progressCfg)
		defer stopProgress()

		var err error
		conversationID, resolveErr := h.resolveAgentConversationID(agentCtx, msg.FromUserID, defaultName, ag)
		if resolveErr != nil {
			reply = renderFinalFailure("", resolveErr)
			h.sendReplyWithMedia(replyCtx, client, msg, defaultName, reply, clientID)
			return
		}
		reply, err = h.chatWithAgentWithProgress(agentCtx, ag, conversationID, text, onProgress)
		if err != nil {
			reply = renderFinalFailure("", err)
		} else {
			h.recordCodexThread(msg.FromUserID, defaultName, ag, conversationID)
			reply = renderFinalSuccess("", reply)
		}
		if activeTask != nil && !activeTask.shouldSendFinal() {
			return
		}
	} else {
		log.Printf("[handler] agent not ready, using echo mode for %s", msg.FromUserID)
		reply = "[echo] " + text
	}

	h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
}

// sendToNamedAgent sends the message to a specific agent and replies.
func (h *Handler) sendToNamedAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, name, message, clientID string) {
	ag, agErr := h.getAgent(ctx, name)
	if agErr != nil {
		log.Printf("[handler] agent %q not available: %v", name, agErr)
		reply := fmt.Sprintf("Agent %q is not available: %v", name, agErr)
		SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	replyCtx := ctx
	progressCfg := h.resolveProgressConfig(name)
	agentCtx, cancelTaskTimeout := contextWithTaskTimeout(ctx, progressCfg)
	defer cancelTaskTimeout()

	executionKey := h.agentExecutionKey(msg.FromUserID, name, ag)
	var activeTask *activeAgentTask
	if isCodexAgent(name, ag.Info()) {
		task, taskCtx, started := h.beginActiveTask(agentCtx, executionKey)
		if !started {
			h.storePendingGuide(executionKey, message)
			_ = SendTextReply(ctx, client, msg.FromUserID, runningCodexGuidePrompt(), msg.ContextToken, clientID)
			return
		}
		activeTask = task
		defer h.finishActiveTask(executionKey, task)
		agentCtx = taskCtx
	}
	unlock := h.lockAgentExecution(executionKey)
	defer unlock()

	onProgress, stopProgress := h.startProgressSession(agentCtx, client, msg.FromUserID, msg.ContextToken, "", message, progressCfg)
	defer stopProgress()

	conversationID, resolveErr := h.resolveAgentConversationID(agentCtx, msg.FromUserID, name, ag)
	if resolveErr != nil {
		reply := renderFinalFailure("["+name+"] ", resolveErr)
		h.sendReplyWithMedia(replyCtx, client, msg, name, reply, clientID)
		return
	}
	reply, err := h.chatWithAgentWithProgress(agentCtx, ag, conversationID, message, onProgress)
	if err != nil {
		reply = renderFinalFailure("["+name+"] ", err)
	} else {
		h.recordCodexThread(msg.FromUserID, name, ag, conversationID)
		reply = renderFinalSuccess("["+name+"] ", reply)
	}
	if activeTask != nil && !activeTask.shouldSendFinal() {
		return
	}
	h.sendReplyWithMedia(replyCtx, client, msg, name, reply, clientID)
}

// broadcastToAgents sends the message to multiple agents in parallel.
// Each reply is sent as a separate message with the agent name prefix.
func (h *Handler) broadcastToAgents(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, names []string, message string) {
	type result struct {
		name  string
		reply string
	}

	ch := make(chan result, len(names))

	for _, name := range names {
		go func(n string) {
			ag, err := h.getAgent(ctx, n)
			if err != nil {
				ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err)}
				return
			}
			progressCfg := h.resolveProgressConfig(n)
			agentCtx, cancelTaskTimeout := contextWithTaskTimeout(ctx, progressCfg)
			defer cancelTaskTimeout()

			executionKey := h.agentExecutionKey(msg.FromUserID, n, ag)
			unlock := h.lockAgentExecution(executionKey)
			defer unlock()

			onProgress, stopProgress := h.startProgressSession(agentCtx, client, msg.FromUserID, msg.ContextToken, "["+n+"] ", message, progressCfg)
			defer stopProgress()

			conversationID, resolveErr := h.resolveAgentConversationID(agentCtx, msg.FromUserID, n, ag)
			if resolveErr != nil {
				ch <- result{name: n, reply: renderFinalFailure("["+n+"] ", resolveErr)}
				return
			}
			reply, err := h.chatWithAgentWithProgress(agentCtx, ag, conversationID, message, onProgress)
			if err != nil {
				ch <- result{name: n, reply: renderFinalFailure("["+n+"] ", err)}
				return
			}
			h.recordCodexThread(msg.FromUserID, n, ag, conversationID)
			ch <- result{name: n, reply: renderFinalSuccess("["+n+"] ", reply)}
		}(name)
	}

	// Send replies as they arrive
	for range names {
		r := <-ch
		clientID := NewClientID()
		h.sendReplyWithMedia(ctx, client, msg, r.name, r.reply, clientID)
	}
}

// sendReplyWithMedia sends a text reply and any extracted image URLs.
func (h *Handler) sendReplyWithMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, agentName, reply, clientID string) {
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
		if err := SendMediaFromPath(ctx, client, msg.FromUserID, attachmentPath, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send attachment to %s: %v", msg.FromUserID, err)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
	}

	reply = rewriteReplyWithAttachmentResults(reply, sentPaths, failedPaths)

	if err := SendTextReplyChunks(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID, textReplyChunkLimit(ctx)); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
	}

	for _, imgURL := range imageURLs {
		if err := SendMediaFromURL(ctx, client, msg.FromUserID, imgURL, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send image to %s: %v", msg.FromUserID, err)
		}
	}
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
	if !isCodexAgent(agentName, ag.Info()) {
		return userID, nil
	}
	workspaceRoot := h.codexWorkspaceRootForUser(userID, agentName, ag)
	bindingKey := codexBindingKey(userID, agentName)
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		h.ensureCodexSessions().ensureWorkspace(bindingKey, workspaceRoot)
		return conversationID, nil
	}
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	if pending {
		codexAg.ClearCodexThread(conversationID)
		return conversationID, nil
	}
	if threadID != "" {
		current, hasCurrent := codexAg.CurrentCodexThread(conversationID)
		if !hasCurrent || current != threadID {
			if err := codexAg.UseCodexThread(ctx, conversationID, threadID); err != nil {
				return "", fmt.Errorf("恢复 Codex 会话失败: %w", err)
			}
		}
	}
	h.ensureCodexSessions().ensureWorkspace(bindingKey, workspaceRoot)
	return conversationID, nil
}

func (h *Handler) recordCodexThread(userID string, agentName string, ag agent.Agent, conversationID string) {
	if !isCodexAgent(agentName, ag.Info()) {
		return
	}
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		return
	}
	threadID, ok := codexAg.CurrentCodexThread(conversationID)
	if !ok {
		return
	}
	workspaceRoot := h.codexWorkspaceRootForUser(userID, agentName, ag)
	bindingKey := codexBindingKey(userID, agentName)
	if ownerWorkspace, ok := h.ensureCodexSessions().findWorkspaceByThread(bindingKey, threadID); ok {
		workspaceRoot = ownerWorkspace
	}
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
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

func (h *Handler) sendProgressMessage(ctx context.Context, client *ilink.Client, userID, contextToken, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	clientID := NewClientID()
	if err := SendTextReply(ctx, client, userID, text, contextToken, clientID); err != nil {
		log.Printf("[handler] failed to send progress message to %s: %v", userID, err)
	}
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
	ag := h.getDefaultAgent()
	if ag == nil {
		return "No agent running."
	}
	name := ag.Info().Name
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
		"model: "+info.Model,
	)
}

func buildHelpText() string {
	return `WeClaw 帮助

常用：

/info 查看当前 Agent

/new 开启新会话

/cwd /绝对路径 切换工作目录

/progress 查看或切换进度模式

Codex：

/codex whoami 查看当前 Codex workspace 和 thread

/codex ls 查看已记录的 workspace 会话

/codex new 新建当前 workspace 的 Codex 会话

/codex switch <编号|threadId> 切换到指定 Codex thread

/guide 将暂存消息作为引导对话发送给正在执行的 Codex

/cancel 撤回暂存的引导消息

Codex 账号：

/sw ls 查看可切换账号

/sw current 查看当前账号

/sw <编号|ID> 切换账号

/sw reload 手动刷新 Codex Agent

/sw help 查看账号切换帮助

指定 Agent：

/codex 任务 发给 Codex

/claude 任务 发给 Claude

@codex @claude 任务 同时发给多个 Agent

常用别名：

/cx = /codex

/cc = /claude

/cs = /cursor

/km = /kimi

/gm = /gemini`
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

func isSwitchCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	return len(fields) > 0 && fields[0] == "/sw"
}

func isProgressCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	return len(fields) > 0 && fields[0] == "/progress"
}

func isCodexSessionCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || fields[0] != "/codex" {
		return false
	}
	switch fields[1] {
	case "whoami", "ls", "new", "switch", "help":
		return true
	default:
		return false
	}
}

func (h *Handler) handleProgressCommand(trimmed string) string {
	fields := strings.Fields(trimmed)
	if len(fields) == 1 {
		return wechatCommandText(
			"当前进度模式："+h.resolveProgressConfig("").Mode,
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

	cfg := h.resolveProgressConfig("")
	cfg.Mode = mode
	h.SetProgressConfig(cfg)
	return "已切换进度模式：" + mode
}

func (h *Handler) handleCodexSessionCommand(ctx context.Context, userID string, trimmed string) string {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || fields[1] == "help" {
		return buildCodexSessionHelpText()
	}

	agentName, ag, err := h.getCodexSessionAgent(ctx)
	if err != nil {
		return err.Error()
	}
	workspaceRoot := h.codexWorkspaceRoot(agentName)
	bindingKey := codexBindingKey(userID, agentName)
	h.ensureCodexSessions().ensureWorkspace(bindingKey, workspaceRoot)
	h.syncCodexThreadFromAgent(userID, agentName, workspaceRoot, ag)

	switch fields[1] {
	case "whoami":
		return h.renderCodexWhoami(bindingKey, workspaceRoot)
	case "ls":
		return h.renderCodexList(bindingKey)
	case "new":
		return h.handleCodexNew(userID, agentName, workspaceRoot, ag)
	case "switch":
		if len(fields) != 3 {
			return "用法: /codex switch <编号|threadId>"
		}
		return h.handleCodexSwitch(ctx, userID, agentName, workspaceRoot, ag, fields[2])
	default:
		return buildCodexSessionHelpText()
	}
}

func (h *Handler) handleCodexNew(userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
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
	if err := codexAg.UseCodexThread(ctx, conversationID, threadID); err != nil {
		return fmt.Sprintf("切换线程失败: %v", err)
	}
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	return wechatCommandText("已切换线程。", "workspace: "+workspaceRoot, "thread: "+threadID)
}

func (h *Handler) resolveCodexSwitchTarget(bindingKey string, agentName string, workspaceRoot string, target string, ag agent.Agent) (string, string, error) {
	target = strings.TrimSpace(target)
	if index, ok := parseCodexListIndex(target); ok {
		views := h.codexSwitchTargets(bindingKey)
		if index < 0 || index >= len(views) {
			return "", "", fmt.Errorf("编号不存在，请先发送 /codex ls 查看可切换会话。")
		}
		view := views[index]
		threadID := strings.TrimSpace(view.ThreadID)
		if threadID == "" || view.PendingNewThread {
			return "", "", fmt.Errorf("编号 %d 当前没有可切换的 thread。", index)
		}
		workspaceRoot = h.switchCodexWorkspace(agentName, view.WorkspaceRoot, ag)
		return workspaceRoot, threadID, nil
	}
	threadID := target
	workspaceRoot = h.resolveCodexSwitchWorkspace(bindingKey, agentName, workspaceRoot, threadID, ag)
	return workspaceRoot, threadID, nil
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
		return h.switchCodexWorkspace(agentName, workspaceRoot, ag)
	}
	if localWorkspace, ok := h.findLocalCodexWorkspaceByThread(threadID); ok {
		return h.switchCodexWorkspace(agentName, localWorkspace, ag)
	}
	return fallbackWorkspace
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

func (h *Handler) renderCodexList(bindingKey string) string {
	views := h.codexSwitchTargets(bindingKey)
	if len(views) == 0 {
		return "当前还没有 Codex workspace。"
	}
	lines := []string{"Codex workspaces:"}
	for index, view := range views {
		lines = append(lines, fmt.Sprintf("%d. %s", index, view.WorkspaceRoot))
		lines = append(lines, "   thread: "+renderCodexThreadLabel(view.ThreadID, view.PendingNewThread))
		if view.ThreadName != "" {
			lines = append(lines, "   名称: "+view.ThreadName)
		}
		if view.Source == codexLocalSource {
			lines = append(lines, "   来源: 本机 Codex")
		}
	}
	return wechatCommandText(lines...)
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
		"/codex whoami",
		"/codex ls",
		"/codex new",
		"/codex switch <编号|threadId>",
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

// parseSwitchCommand 将微信命令转换为 codex-switch 脚本参数。
func parseSwitchCommand(trimmed string) ([]string, string) {
	fields := strings.Fields(trimmed)
	if len(fields) == 0 || fields[0] != "/sw" {
		return nil, switchCommandUsage
	}
	if len(fields) == 1 {
		return nil, switchCommandUsage
	}

	cmd := fields[1]
	switch cmd {
	case "ls", "list":
		if len(fields) != 2 {
			return nil, switchCommandUsage
		}
		return []string{"list"}, ""
	case "current", "help", "config":
		if len(fields) != 2 {
			return nil, switchCommandUsage
		}
		return []string{cmd}, ""
	case "reload", "refresh", "restart":
		if len(fields) != 2 {
			return nil, switchCommandUsage
		}
		return []string{"reload"}, ""
	case "show", "switch":
		if len(fields) != 3 {
			return nil, switchCommandUsage
		}
		return []string{cmd, fields[2]}, ""
	default:
		// /sw 0 或 /sw provider-id 走快捷切换模式
		if len(fields) != 2 {
			return nil, switchCommandUsage
		}
		return []string{"switch", cmd}, ""
	}
}

func (h *Handler) handleSwitchCommand(ctx context.Context, trimmed string) string {
	args, usage := parseSwitchCommand(trimmed)
	if usage != "" {
		return usage
	}
	if len(args) == 1 && args[0] == "help" {
		return buildSwitchHelpText()
	}
	if isCodexReloadAction(args) {
		return h.refreshCodexAgentsAfterSwitch(ctx)
	}

	runner := h.switchRunner
	if runner == nil {
		runner = defaultSwitchCommandRunner
	}

	script := h.switchScript
	if strings.TrimSpace(script) == "" {
		script = resolveSwitchScriptPath()
	}

	runCtx, cancel := context.WithTimeout(ctx, switchCommandTimeout)
	defer cancel()

	output, err := runner(runCtx, script, args...)
	cleanOutput := strings.TrimSpace(stripANSI(output))
	if err != nil {
		if cleanOutput == "" {
			return fmt.Sprintf("切换失败: %v", err)
		}
		return wechatCommandText("切换失败:", cleanOutput)
	}
	if cleanOutput == "" {
		cleanOutput = "命令执行完成。"
	}
	if isCodexSwitchAction(args) {
		return wechatCommandText(cleanOutput, h.refreshCodexAgentsAfterSwitch(ctx))
	}
	return wechatCommandText(cleanOutput)
}

func isCodexSwitchAction(args []string) bool {
	return len(args) > 0 && args[0] == "switch"
}

// isCodexReloadAction 识别只刷新 WeClaw 进程、不执行外部切号脚本的命令。
func isCodexReloadAction(args []string) bool {
	return len(args) == 1 && args[0] == "reload"
}

func (h *Handler) refreshCodexAgentsAfterSwitch(ctx context.Context) string {
	stoppedNames := h.stopRunningCodexAgents()
	if len(stoppedNames) == 0 {
		return "当前没有运行中的 Codex Agent，下一次 Codex 请求会使用当前本机登录状态。"
	}

	if defaultName, ok := h.defaultAgentNameForRestart(stoppedNames); ok {
		if _, err := h.getAgent(ctx, defaultName); err != nil {
			return fmt.Sprintf("已停止旧 Codex Agent，但重启默认 Codex Agent 失败：%v", err)
		}
		return "已刷新 WeClaw 中的 Codex Agent，下一次请求会使用当前本机登录状态。"
	}
	return "已停止旧 Codex Agent，下一次 Codex 请求会使用当前本机登录状态。"
}

func (h *Handler) stopRunningCodexAgents() []string {
	type runningAgent struct {
		name string
		ag   agent.Agent
	}

	h.mu.Lock()
	var targets []runningAgent
	for name, ag := range h.agents {
		if !isCodexAgent(name, ag.Info()) {
			continue
		}
		targets = append(targets, runningAgent{name: name, ag: ag})
		delete(h.agents, name)
	}
	h.mu.Unlock()

	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.name)
		if stopper, ok := target.ag.(StoppableAgent); ok {
			stopper.Stop()
		}
	}
	return names
}

func (h *Handler) defaultAgentNameForRestart(stoppedNames []string) (string, bool) {
	h.mu.RLock()
	defaultName := h.defaultName
	hasFactory := h.factory != nil
	h.mu.RUnlock()
	if defaultName == "" || !hasFactory {
		return "", false
	}
	for _, name := range stoppedNames {
		if name == defaultName {
			return defaultName, true
		}
	}
	return "", false
}

func isCodexAgent(name string, info agent.AgentInfo) bool {
	if strings.EqualFold(name, "codex") || strings.EqualFold(info.Name, "codex") {
		return true
	}
	command := strings.ToLower(filepath.Base(info.Command))
	return strings.Contains(command, "codex")
}

func buildSwitchHelpText() string {
	return wechatCommandText(
		"Codex 账户切换命令:",
		"/sw ls - 列出可切换账户",
		"/sw current - 显示当前账户",
		"/sw <编号|ID> - 切换到指定账户",
		"/sw reload - 手动刷新 WeClaw 中的 Codex Agent",
		"/sw show <编号|ID> - 查看账户详情",
		"/sw config - 查看当前 Codex 配置",
		"/sw help - 显示本帮助",
	)
}

func resolveSwitchScriptPath() string {
	if path := strings.TrimSpace(os.Getenv(switchScriptEnvVar)); path != "" {
		return path
	}
	if _, err := os.Stat(switchScriptDefaultPath); err == nil {
		return switchScriptDefaultPath
	}
	return "codex-switch.sh"
}

func defaultSwitchCommandRunner(ctx context.Context, scriptPath string, args ...string) (string, error) {
	cmdArgs := append([]string{scriptPath}, args...)
	cmd := exec.CommandContext(ctx, "/bin/bash", cmdArgs...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func stripANSI(text string) string {
	return ansiEscapePattern.ReplaceAllString(text, "")
}

func extractText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	return ""
}

func extractImage(msg ilink.WeixinMessage) *ilink.ImageItem {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeImage && item.ImageItem != nil {
			return item.ImageItem
		}
	}
	return nil
}

func extractFile(msg ilink.WeixinMessage) *ilink.FileItem {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeFile && item.FileItem != nil {
			return item.FileItem
		}
	}
	return nil
}

func extractVoiceText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Text != "" {
			return item.VoiceItem.Text
		}
	}
	return ""
}

func (h *Handler) handleFileInput(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, file *ilink.FileItem, text string) (string, bool) {
	saved, err := h.saveIncomingFile(ctx, file)
	if err != nil {
		log.Printf("[handler] failed to save incoming file from %s: %v", msg.FromUserID, err)
		reply := fmt.Sprintf("文件保存失败：%v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, NewClientID())
		return "", false
	}
	log.Printf("[handler] saved incoming file from %s: %s", msg.FromUserID, saved.path)
	return buildFileAgentMessage(text, saved), true
}

type savedIncomingFile struct {
	name string
	path string
}

func (h *Handler) saveIncomingFile(ctx context.Context, file *ilink.FileItem) (savedIncomingFile, error) {
	if file == nil || file.Media == nil || file.Media.EncryptQueryParam == "" || file.Media.AESKey == "" {
		return savedIncomingFile{}, fmt.Errorf("文件缺少下载信息")
	}
	downloader := h.cdnDownloader
	if downloader == nil {
		downloader = DownloadFileFromCDN
	}
	data, err := downloader(ctx, file.Media.EncryptQueryParam, file.Media.AESKey)
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

func (h *Handler) handleImageSave(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, img *ilink.ImageItem) {
	clientID := NewClientID()
	log.Printf("[handler] received image from %s, saving to %s", msg.FromUserID, h.saveDir)

	// Download image data
	var data []byte
	var err error

	if img.URL != "" {
		// Direct URL download
		data, _, err = downloadFile(ctx, img.URL)
	} else if img.Media != nil && img.Media.EncryptQueryParam != "" {
		// CDN encrypted download
		data, err = DownloadFileFromCDN(ctx, img.Media.EncryptQueryParam, img.Media.AESKey)
	} else {
		log.Printf("[handler] image has no URL or media info from %s", msg.FromUserID)
		return
	}

	if err != nil {
		log.Printf("[handler] failed to download image from %s: %v", msg.FromUserID, err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	// Detect extension from content
	ext := detectImageExt(data)

	// Generate filename with timestamp
	ts := time.Now().Format("20060102-150405")
	fileName := fmt.Sprintf("%s%s", ts, ext)
	filePath := filepath.Join(h.saveDir, fileName)

	// Ensure save directory exists
	if err := os.MkdirAll(h.saveDir, 0o755); err != nil {
		log.Printf("[handler] failed to create save dir: %v", err)
		return
	}

	// Write image file
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		log.Printf("[handler] failed to write image: %v", err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	// Write sidecar file
	sidecarPath := filePath + ".sidecar.md"
	sidecarContent := fmt.Sprintf("---\nid: %s\n---\n", uuid.New().String())
	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o644); err != nil {
		log.Printf("[handler] failed to write sidecar: %v", err)
	}

	log.Printf("[handler] saved image to %s (%d bytes)", filePath, len(data))
	reply := fmt.Sprintf("Saved: %s", fileName)
	if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
	}
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
