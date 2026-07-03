package agent

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	acpScannerInitialBufferSize = 4 * 1024 * 1024
	acpScannerMaxTokenSize      = 64 * 1024 * 1024
)

// ACPAgent communicates with ACP-compatible agents (claude-agent-acp, codex-acp, cursor agent, etc.) via stdio JSON-RPC 2.0.
type ACPAgent struct {
	command        string
	args           []string
	model          string
	effort         string
	approvalPolicy string
	sandboxMode    string
	systemPrompt   string
	cwd            string
	env            map[string]string
	runAs          runAsUserSpec
	protocol       string // "legacy_acp" or "codex_app_server"

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	scanner  *bufio.Scanner
	started  bool
	nextID   atomic.Int64
	sessions map[string]string // conversationID -> sessionID (legacy ACP)
	threads  map[string]string // conversationID -> threadID (codex app-server)
	// resumeOnFirstUse marks restored thread mappings that should trigger a
	// best-effort thread/resume call before first turn.
	resumeOnFirstUse            map[string]bool // conversationID -> resume needed
	usageLimitRefreshOnNextTurn map[string]bool // conversationID -> refresh runtime before next turn
	conversationCwds            map[string]string
	stateFile                   string // optional persisted state file path
	history                     map[string][]acpHistoryMessage

	// pending tracks in-flight JSON-RPC requests
	pendingMu sync.Mutex
	pending   map[int64]chan *rpcResponse

	// notifications channel for session/update events
	notifyMu sync.Mutex
	notifyCh map[string]chan *sessionUpdate // sessionID -> channel
	turnCh   map[string]chan *codexTurnEvent

	stderr *acpStderrWriter // captures stderr for error reporting

	// rpcCall allows tests to stub JSON-RPC interactions without a subprocess.
	rpcCall func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
}

// ACPAgentConfig holds configuration for the ACP agent.
type ACPAgentConfig struct {
	Command        string   // path to ACP agent binary (claude-agent-acp, codex-acp, cursor agent, etc.)
	Args           []string // extra args for command (e.g. ["acp"] for cursor)
	Model          string
	Effort         string
	ApprovalPolicy string
	SandboxMode    string
	SystemPrompt   string
	Cwd            string            // working directory
	Env            map[string]string // extra environment variables
	StateFile      string            // optional persisted mapping file path
	RunAsUser      string            // 以独立 Unix 用户运行（文件系统隔离）
	RunAsEnv       []string          // run_as_user 时透传的环境变量名白名单
}

// --- JSON-RPC types ---

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// --- ACP protocol types ---

type initParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
}

type clientCapabilities struct {
	FS *fsCapabilities `json:"fs,omitempty"`
}

type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type newSessionParams struct {
	Cwd        string        `json:"cwd"`
	McpServers []interface{} `json:"mcpServers"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

type promptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []promptEntry `json:"prompt"`
}

type promptEntry struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

type sessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    sessionUpdate `json:"update"`
}

type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"`
	// For agent_message_chunk
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type permissionRequestParams struct {
	ThreadID           string              `json:"threadId,omitempty"`
	TurnID             string              `json:"turnId,omitempty"`
	ToolCall           json.RawMessage     `json:"toolCall"`
	Command            permissionCommand   `json:"command,omitempty"`
	Cwd                string              `json:"cwd,omitempty"`
	Options            []permissionOption  `json:"options"`
	AvailableDecisions permissionDecisions `json:"availableDecisions,omitempty"`
}

type permissionCommand []string

type permissionDecisions []string

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type permissionResponseFormat string

const (
	permissionResponseOutcome  permissionResponseFormat = "outcome"
	permissionResponseDecision permissionResponseFormat = "decision"
)

// Codex app-server protocol constants and types.
const (
	protocolLegacyACP      = "legacy_acp"
	protocolCodexAppServer = "codex_app_server"
)

const acpPersistedStateVersion = 1

type acpPersistedState struct {
	Version  int                            `json:"version"`
	Protocol string                         `json:"protocol"`
	Sessions map[string]string              `json:"sessions,omitempty"`
	Threads  map[string]string              `json:"threads,omitempty"`
	History  map[string][]acpHistoryMessage `json:"history,omitempty"`
	Updated  string                         `json:"updatedAt,omitempty"`
}

type acpHistoryMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

const (
	acpMaxHistoryMessages      = 20
	acpMaxRehydratePromptChars = 12000
)

type codexTurnStartParams struct {
	ThreadID       string           `json:"threadId"`
	ApprovalPolicy string           `json:"approvalPolicy,omitempty"`
	Input          []codexUserInput `json:"input"`
	SandboxPolicy  interface{}      `json:"sandboxPolicy,omitempty"`
	Model          string           `json:"model,omitempty"`
	Effort         string           `json:"effort,omitempty"`
	Cwd            string           `json:"cwd,omitempty"`
}

type codexUserInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type codexTurnEvent struct {
	Kind     string
	ItemID   string
	Delta    string
	Text     string
	Approval *codexApprovalRequest
}

type codexApprovalRequest struct {
	ID             json.RawMessage
	ResponseFormat permissionResponseFormat
	Request        ApprovalRequest
}

type codexFinalAssembler struct {
	order           []string
	deltasByItem    map[string][]string
	snapshotsByItem map[string]string
	completedByItem map[string]string
}

func newCodexFinalAssembler() *codexFinalAssembler {
	return &codexFinalAssembler{
		deltasByItem:    make(map[string][]string),
		snapshotsByItem: make(map[string]string),
		completedByItem: make(map[string]string),
	}
}

func (a *codexFinalAssembler) addDelta(itemID string, text string) {
	itemID = normalizeCodexItemID(itemID)
	a.rememberItem(itemID)
	a.deltasByItem[itemID] = append(a.deltasByItem[itemID], text)
}

func (a *codexFinalAssembler) addSnapshot(itemID string, text string) {
	itemID = normalizeCodexItemID(itemID)
	a.rememberItem(itemID)
	a.snapshotsByItem[itemID] = text
}

func (a *codexFinalAssembler) addCompleted(itemID string, text string) {
	itemID = normalizeCodexItemID(itemID)
	a.rememberItem(itemID)
	a.completedByItem[itemID] = text
}

func (a *codexFinalAssembler) finalText() string {
	for i := len(a.order) - 1; i >= 0; i-- {
		if text := a.itemText(a.order[i]); text != "" {
			return text
		}
	}
	return ""
}

func (a *codexFinalAssembler) itemText(itemID string) string {
	if deltas := a.deltasByItem[itemID]; len(deltas) > 0 {
		return strings.TrimSpace(strings.Join(deltas, ""))
	}
	if text := strings.TrimSpace(a.completedByItem[itemID]); text != "" {
		return text
	}
	return strings.TrimSpace(a.snapshotsByItem[itemID])
}

func (a *codexFinalAssembler) rememberItem(itemID string) {
	for _, existing := range a.order {
		if existing == itemID {
			return
		}
	}
	a.order = append(a.order, itemID)
}

func normalizeCodexItemID(itemID string) string {
	if itemID == "" {
		return "__default__"
	}
	return itemID
}

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

// NewACPAgent creates a new ACP agent.
func NewACPAgent(cfg ACPAgentConfig) *ACPAgent {
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
	}
	a.loadState()
	return a
}

// Start launches the claude-agent-acp subprocess and initializes the connection.
func (a *ACPAgent) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}

	a.cmd = exec.CommandContext(ctx, a.command, a.args...)
	a.cmd.Dir = a.cwd
	if command, cmdArgs := a.runAs.wrapCommand(a.command, a.args); command != a.command {
		a.cmd = exec.CommandContext(ctx, command, cmdArgs...)
		a.cmd.Dir = a.cwd
	}
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			a.mu.Unlock()
			return fmt.Errorf("build acp env: %w", err)
		}
		a.cmd.Env = cmdEnv
	}
	// Capture stderr for debugging and error reporting
	a.stderr = &acpStderrWriter{prefix: "[acp-stderr]"}
	a.cmd.Stderr = a.stderr

	var err error
	a.stdin, err = a.cmd.StdinPipe()
	if err != nil {
		a.mu.Unlock()
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	stdout, err := a.cmd.StdoutPipe()
	if err != nil {
		a.mu.Unlock()
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := a.cmd.Start(); err != nil {
		a.mu.Unlock()
		return fmt.Errorf("start acp agent %s: %w", a.command, err)
	}

	pid := a.cmd.Process.Pid
	log.Printf("[acp] started subprocess (command=%s, pid=%d)", a.command, pid)

	a.scanner = newACPScanner(stdout)
	a.started = true

	// Start reading loop
	go a.readLoop()

	// Release lock before calling initialize — call() needs a.mu to write to stdin
	a.mu.Unlock()

	// Initialize handshake with timeout
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	log.Printf("[acp] sending initialize handshake (pid=%d, protocol=%s)...", pid, a.protocol)
	var result json.RawMessage
	if a.protocol == protocolCodexAppServer {
		result, err = a.rpc(initCtx, "initialize", codexInitializeParams())
		if err == nil {
			// codex app-server expects an "initialized" notification after initialize response
			err = a.notify("initialized", nil)
		}
	} else {
		result, err = a.rpc(initCtx, "initialize", initParams{
			ProtocolVersion: 1,
			ClientCapabilities: clientCapabilities{
				FS: &fsCapabilities{ReadTextFile: true, WriteTextFile: true},
			},
		})
	}
	if err != nil {
		a.mu.Lock()
		a.started = false
		stdin := a.stdin
		cmd := a.cmd
		a.stdin = nil
		a.cmd = nil
		a.scanner = nil
		a.mu.Unlock()
		stopACPProcess(stdin, cmd)
		// Use stderr detail if available (e.g. "connect ECONNREFUSED")
		if detail := a.stderr.LastError(); detail != "" {
			return fmt.Errorf("agent startup failed: %s", detail)
		}
		// Provide a helpful hint when the binary looks like a Claude CLI that doesn't support ACP
		base := strings.ToLower(filepath.Base(a.command))
		if base == "claude" || base == "claude.exe" {
			return fmt.Errorf("agent startup failed (pid=%d): %w\n\nHint: the 'claude' CLI does not support ACP protocol directly.\nSet type to \"cli\" in your config, or install claude-agent-acp and set command to \"claude-agent-acp\".", pid, err)
		}
		return fmt.Errorf("agent startup failed (pid=%d): %w", pid, err)
	}

	log.Printf("[acp] initialized (pid=%d): %s", pid, string(result))
	return nil
}

// stopACPProcess 关闭 ACP 子进程资源；启动失败和显式 Stop 都必须容忍 readLoop 已经清理状态。
func stopACPProcess(stdin io.Closer, cmd *exec.Cmd) {
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

func codexInitializeParams() map[string]interface{} {
	return map[string]interface{}{
		"clientInfo": map[string]string{"name": "weclaw", "version": "0.3.0"},
		"capabilities": map[string]interface{}{
			"experimentalApi": true,
		},
	}
}

// newACPScanner 创建 ACP stdout 读取器；Codex MCP 启动状态可能输出较大的单行 JSON。
func newACPScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, acpScannerInitialBufferSize), acpScannerMaxTokenSize)
	return scanner
}

// Stop terminates the subprocess.
func (a *ACPAgent) Stop() {
	a.mu.Lock()
	if !a.started && a.stdin == nil && a.cmd == nil {
		a.mu.Unlock()
		return
	}
	stdin := a.stdin
	cmd := a.cmd
	a.started = false
	a.stdin = nil
	a.cmd = nil
	a.scanner = nil
	a.mu.Unlock()

	stopACPProcess(stdin, cmd)
}

// SetCwd changes the working directory for subsequent sessions.
func (a *ACPAgent) SetCwd(cwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cwd = cwd
}

// SetConversationCwd 固定单个 conversation 的工作目录，避免后台任务被全局 cwd 切换影响。
func (a *ACPAgent) SetConversationCwd(conversationID string, cwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		delete(a.conversationCwds, conversationID)
		return
	}
	a.conversationCwds[conversationID] = cwd
}

func (a *ACPAgent) cwdForConversation(conversationID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cwd := strings.TrimSpace(a.conversationCwds[conversationID]); cwd != "" {
		return cwd
	}
	return a.cwd
}

// ResetSession clears the existing session for the given conversationID and
// immediately creates a new one, returning the new session ID.
func (a *ACPAgent) ResetSession(ctx context.Context, conversationID string) (string, error) {
	if err := a.ensureStarted(ctx); err != nil {
		return "", err
	}

	if a.protocol == protocolCodexAppServer {
		a.mu.Lock()
		delete(a.threads, conversationID)
		delete(a.resumeOnFirstUse, conversationID)
		delete(a.history, conversationID)
		a.mu.Unlock()
		a.persistState()
		log.Printf("[acp] thread reset (conversation=%s), creating new thread", conversationID)

		return a.createResetCodexThread(ctx, conversationID)
	}

	a.mu.Lock()
	delete(a.sessions, conversationID)
	delete(a.history, conversationID)
	a.mu.Unlock()
	a.persistState()
	log.Printf("[acp] session reset (conversation=%s), creating new session", conversationID)

	sessionID, _, err := a.getOrCreateSession(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("create new session: %w", err)
	}
	return sessionID, nil
}

// ensureStarted 确保重置会话前有可写的真实 runtime；测试桩直接走 rpcCall。
func (a *ACPAgent) ensureStarted(ctx context.Context) error {
	if a.rpcCall != nil {
		return nil
	}
	a.mu.Lock()
	started := a.started
	a.mu.Unlock()
	if started {
		return nil
	}
	return a.Start(ctx)
}

// createResetCodexThread 处理 /new 的新 thread 创建，并在 stdin 关闭时重启一次。
func (a *ACPAgent) createResetCodexThread(ctx context.Context, conversationID string) (string, error) {
	threadID, _, err := a.getOrCreateThread(ctx, conversationID)
	if err == nil {
		return threadID, nil
	}
	if !isClosedStdinError(err) {
		return "", fmt.Errorf("create new thread: %w", err)
	}
	log.Printf("[acp] codex stdin is closed during reset, restarting runtime (conversation=%s): %v", conversationID, err)
	a.Stop()
	if err := a.Start(ctx); err != nil {
		return "", fmt.Errorf("restart codex runtime: %w", err)
	}
	threadID, _, err = a.getOrCreateThread(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("create new thread after runtime restart: %w", err)
	}
	return threadID, nil
}

// isClosedStdinError 判断 JSON-RPC 写入失败是否来自已失效的子进程 stdin。
func isClosedStdinError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "write to stdin") &&
		(strings.Contains(text, "file already closed") ||
			strings.Contains(text, "broken pipe") ||
			strings.Contains(text, "closed pipe") ||
			strings.Contains(text, "acp runtime is not running"))
}

// CurrentCodexThread 返回指定会话当前绑定的 Codex thread。
func (a *ACPAgent) CurrentCodexThread(conversationID string) (string, bool) {
	if a.protocol != protocolCodexAppServer {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	threadID := strings.TrimSpace(a.threads[conversationID])
	return threadID, threadID != ""
}

// UseCodexThread 将指定会话切换到已有 Codex thread，并先 resume 验证可用性。
func (a *ACPAgent) UseCodexThread(ctx context.Context, conversationID string, threadID string) error {
	if a.protocol != protocolCodexAppServer {
		return fmt.Errorf("agent is not codex app-server")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return fmt.Errorf("empty thread id")
	}
	if err := a.resumeThread(ctx, conversationID, threadID); err != nil {
		return fmt.Errorf("resume thread %s: %w", threadID, err)
	}
	a.mu.Lock()
	a.threads[conversationID] = threadID
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	a.persistState()
	return nil
}

// ClearCodexThread 清理指定会话的 Codex thread，下一条消息会创建新 thread。
func (a *ACPAgent) ClearCodexThread(conversationID string) {
	if a.protocol != protocolCodexAppServer {
		return
	}
	a.clearCodexThread(conversationID)
}

// Chat sends a message and returns the full response.
func (a *ACPAgent) Chat(ctx context.Context, conversationID string, message string) (string, error) {
	return a.chat(ctx, conversationID, message, nil)
}

// ChatWithProgress sends a message and emits incremental deltas during generation.
func (a *ACPAgent) ChatWithProgress(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	return a.chat(ctx, conversationID, message, onProgress)
}

func (a *ACPAgent) chat(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	if !a.isRuntimeStarted() {
		if err := a.Start(ctx); err != nil {
			return "", err
		}
	}

	// Route to codex app-server protocol if applicable
	if a.protocol == protocolCodexAppServer {
		return a.chatCodexAppServer(ctx, conversationID, message, onProgress)
	}

	return a.chatLegacyACP(ctx, conversationID, message, onProgress, true)
}

// isRuntimeStarted 在锁内读取 ACP 运行时状态，避免 readLoop 清理状态时并发读写。
func (a *ACPAgent) isRuntimeStarted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started
}

// runtimePID 返回当前子进程 PID；运行时已退出时返回 0 供日志使用。
func (a *ACPAgent) runtimePID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return 0
	}
	return a.cmd.Process.Pid
}

// chatLegacyACP 处理标准 ACP session/prompt 流程，并在会话失效时允许一次重建重试。
func (a *ACPAgent) chatLegacyACP(ctx context.Context, conversationID string, message string, onProgress func(delta string), allowSessionRetry bool) (string, error) {
	// Get or create session
	sessionID, isNew, err := a.getOrCreateSession(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("session error: %w", err)
	}

	pid := a.runtimePID()
	if isNew {
		log.Printf("[acp] new session created (pid=%d, session=%s, conversation=%s)", pid, sessionID, conversationID)
	} else {
		log.Printf("[acp] reusing session (pid=%d, session=%s, conversation=%s)", pid, sessionID, conversationID)
	}

	// Register notification channel for this session
	notifyCh := make(chan *sessionUpdate, 256)
	a.notifyMu.Lock()
	a.notifyCh[sessionID] = notifyCh
	a.notifyMu.Unlock()

	defer func() {
		a.notifyMu.Lock()
		delete(a.notifyCh, sessionID)
		a.notifyMu.Unlock()
	}()

	// Send prompt (this blocks until the prompt completes)
	type promptDoneMsg struct {
		result json.RawMessage
		err    error
	}
	promptDone := make(chan promptDoneMsg, 1)
	go func() {
		result, err := a.rpc(ctx, "session/prompt", promptParams{
			SessionID: sessionID,
			Prompt:    []promptEntry{{Type: "text", Text: message}},
		})
		if result != nil {
			log.Printf("[acp] prompt result (session=%s): %s", sessionID, string(result))
		}
		promptDone <- promptDoneMsg{result: result, err: err}
	}()

	// Collect text chunks from notifications
	var textParts []string

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case update := <-notifyCh:
			if update.SessionUpdate == "agent_message_chunk" {
				text := extractChunkText(update)
				if text != "" {
					textParts = append(textParts, text)
					if onProgress != nil {
						onProgress(text)
					}
				}
			}
		case done := <-promptDone:
			// Drain remaining notifications
			for {
				select {
				case update := <-notifyCh:
					if update.SessionUpdate == "agent_message_chunk" {
						text := extractChunkText(update)
						if text != "" {
							textParts = append(textParts, text)
							if onProgress != nil {
								onProgress(text)
							}
						}
					}
				default:
					goto drained
				}
			}
		drained:
			if done.err != nil {
				if allowSessionRetry && isMissingThreadError(done.err) {
					log.Printf("[acp] stale ACP session detected, retrying with a fresh session (conversation=%s, oldSession=%s): %v", conversationID, sessionID, done.err)
					a.clearACPSession(conversationID)
					return a.chatLegacyACP(ctx, conversationID, message, onProgress, false)
				}
				return "", fmt.Errorf("prompt error: %w", done.err)
			}
			result := strings.TrimSpace(strings.Join(textParts, ""))
			if result == "" {
				// Try extracting from prompt result (some agents return content here)
				result = extractPromptResultText(done.result)
			}
			if result == "" {
				return "", fmt.Errorf("agent returned empty response")
			}
			return result, nil
		}
	}
}

// clearACPSession 删除旧 ACP session 映射，避免恢复到服务端已经不存在的 session。
func (a *ACPAgent) clearACPSession(conversationID string) string {
	a.mu.Lock()
	oldSessionID := a.sessions[conversationID]
	delete(a.sessions, conversationID)
	a.mu.Unlock()
	a.persistState()
	return oldSessionID
}

func (a *ACPAgent) getOrCreateSession(ctx context.Context, conversationID string) (string, bool, error) {
	a.mu.Lock()
	sid, exists := a.sessions[conversationID]
	a.mu.Unlock()

	if exists {
		return sid, false, nil
	}

	result, err := a.rpc(ctx, "session/new", newSessionParams{
		Cwd:        a.cwdForConversation(conversationID),
		McpServers: []interface{}{},
	})
	if err != nil {
		return "", false, err
	}

	var sessionResult newSessionResult
	if err := json.Unmarshal(result, &sessionResult); err != nil {
		return "", false, fmt.Errorf("parse session result: %w", err)
	}

	a.mu.Lock()
	a.sessions[conversationID] = sessionResult.SessionID
	a.mu.Unlock()
	a.persistState()

	return sessionResult.SessionID, true, nil
}

// --- Codex app-server protocol ---

func (a *ACPAgent) getOrCreateThread(ctx context.Context, conversationID string) (string, bool, error) {
	a.mu.Lock()
	tid, exists := a.threads[conversationID]
	shouldResume := exists && a.resumeOnFirstUse[conversationID]
	if shouldResume {
		delete(a.resumeOnFirstUse, conversationID)
	}
	a.mu.Unlock()

	if exists {
		if shouldResume {
			if err := a.resumeThread(ctx, conversationID, tid); err != nil {
				log.Printf("[acp] failed to resume restored thread (conversation=%s, thread=%s): %v", conversationID, tid, err)
			} else {
				log.Printf("[acp] restored thread resumed (conversation=%s, thread=%s)", conversationID, tid)
			}
		}
		return tid, false, nil
	}

	params := map[string]interface{}{
		"approvalPolicy":         a.approvalPolicyForContext(ctx),
		"cwd":                    a.cwdForConversation(conversationID),
		"sandbox":                a.sandboxModeForCodex(),
		"persistExtendedHistory": true,
	}
	if a.model != "" {
		params["model"] = a.model
	}
	if a.effort != "" {
		params["effort"] = a.effort
	}
	result, err := a.rpc(ctx, "thread/start", params)
	if err != nil {
		return "", false, err
	}

	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &threadResult); err != nil {
		return "", false, fmt.Errorf("parse thread/start result: %w", err)
	}
	if threadResult.Thread.ID == "" {
		return "", false, fmt.Errorf("thread/start returned empty thread id")
	}

	a.mu.Lock()
	a.threads[conversationID] = threadResult.Thread.ID
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	a.persistState()

	return threadResult.Thread.ID, true, nil
}

func (a *ACPAgent) resumeThread(ctx context.Context, conversationID string, threadID string) error {
	if threadID == "" {
		return fmt.Errorf("empty thread id")
	}

	params := map[string]interface{}{
		"threadId":       threadID,
		"approvalPolicy": a.approvalPolicyForContext(ctx),
		"cwd":            a.cwdForConversation(conversationID),
		"sandbox":        a.sandboxModeForCodex(),
	}
	if a.model != "" {
		params["model"] = a.model
	}
	if a.effort != "" {
		params["effort"] = a.effort
	}

	result, err := a.rpc(ctx, "thread/resume", params)
	if err != nil {
		return err
	}

	var resumeResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &resumeResult); err == nil && resumeResult.Thread.ID != "" && resumeResult.Thread.ID != threadID {
		log.Printf("[acp] thread/resume returned different id (requested=%s, returned=%s)", threadID, resumeResult.Thread.ID)
	}
	return nil
}

func (a *ACPAgent) chatCodexAppServer(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	result, err := a.chatCodexAppServerWithRetry(ctx, conversationID, message, true, onProgress)
	if err != nil {
		return "", err
	}
	a.recordConversationExchange(conversationID, message, result)
	return result, nil
}

func (a *ACPAgent) chatCodexAppServerWithRetry(ctx context.Context, conversationID string, message string, allowFreshRetry bool, onProgress func(delta string)) (string, error) {
	if err := a.refreshCodexRuntimeAfterUsageLimit(ctx, conversationID); err != nil {
		return "", err
	}
	threadID, isNew, err := a.getOrCreateThread(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("thread error: %w", err)
	}

	pid := 0
	a.mu.Lock()
	if a.cmd != nil && a.cmd.Process != nil {
		pid = a.cmd.Process.Pid
	}
	a.mu.Unlock()

	if isNew {
		log.Printf("[acp] new thread created (pid=%d, thread=%s, conversation=%s)", pid, threadID, conversationID)
	} else {
		log.Printf("[acp] reusing thread (pid=%d, thread=%s, conversation=%s)", pid, threadID, conversationID)
	}

	// Register turn event channel
	turnCh := make(chan *codexTurnEvent, 256)
	a.notifyMu.Lock()
	a.turnCh[threadID] = turnCh
	a.notifyMu.Unlock()

	defer func() {
		a.notifyMu.Lock()
		delete(a.turnCh, threadID)
		a.notifyMu.Unlock()
	}()

	// Start turn (call returns quickly with turn info, actual content comes via events)
	go func() {
		startTurn := func() error {
			_, err := a.rpc(ctx, "turn/start", codexTurnStartParams{
				ThreadID:       threadID,
				ApprovalPolicy: a.approvalPolicyForContext(ctx),
				Input:          []codexUserInput{{Type: "text", Text: message}},
				SandboxPolicy:  map[string]interface{}{"type": a.sandboxPolicyTypeForCodex()},
				Model:          a.model,
				Effort:         a.effort,
				Cwd:            a.cwdForConversation(conversationID),
			})
			return err
		}

		err := startTurn()
		if err != nil && isMissingThreadError(err) {
			log.Printf("[acp] turn/start failed with missing thread, attempting thread/resume (thread=%s): %v", threadID, err)
			if resumeErr := a.resumeThread(ctx, conversationID, threadID); resumeErr == nil {
				err = startTurn()
			} else {
				err = fmt.Errorf("%w (resume failed: %v)", err, resumeErr)
			}
		}
		if err != nil {
			// If call itself fails, signal via turn channel
			turnCh <- &codexTurnEvent{Kind: "error", Text: err.Error()}
		}
	}()

	// 汇总同一 turn 内的文本事件，避免 snapshot 和 delta 同时出现时重复拼接。
	assembler := newCodexFinalAssembler()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case evt := <-turnCh:
			if evt.Approval != nil {
				optionID := a.resolvePermissionOption(ctx, evt.Approval.Request)
				if err := a.respondPermissionRequest(evt.Approval.ID, optionID, evt.Approval.ResponseFormat); err != nil {
					return "", fmt.Errorf("approval response error: %w", err)
				}
				continue
			}
			if evt.Kind == "error" {
				if allowFreshRetry && !isNew && isMissingThreadError(fmt.Errorf("%s", evt.Text)) {
					log.Printf("[acp] stale thread error detected, retrying with a fresh thread (conversation=%s, oldThread=%s)", conversationID, threadID)
					return a.retryWithFreshThread(ctx, conversationID, message, "stale_thread_error", onProgress)
				}
				if isCodexAuthStateError(evt.Text) {
					a.invalidateCodexRuntime(conversationID, "auth_state_error")
					return "", fmt.Errorf("turn error: %s；已刷新 Codex 进程，请重试当前消息", evt.Text)
				}
				if isCodexUsageLimitError(evt.Text) {
					a.markCodexUsageLimitRefresh(conversationID)
					return "", fmt.Errorf("turn error: %s；如果你已经手动切换 Codex 账号，下一次请求会刷新 Codex 进程并创建新会话", evt.Text)
				}
				return "", fmt.Errorf("turn error: %s", evt.Text)
			}
			if evt.Delta != "" {
				assembler.addDelta(evt.ItemID, evt.Delta)
				if onProgress != nil {
					onProgress(evt.Delta)
				}
			}
			if evt.Text != "" {
				if evt.Kind == "item_completed" {
					assembler.addCompleted(evt.ItemID, evt.Text)
				} else {
					assembler.addSnapshot(evt.ItemID, evt.Text)
				}
				if onProgress != nil {
					onProgress(evt.Text)
				}
			}
			if evt.Kind == "completed" {
				result := assembler.finalText()
				if result == "" {
					if allowFreshRetry && !isNew {
						log.Printf("[acp] empty response on reused thread, retrying with a fresh thread (conversation=%s, oldThread=%s)", conversationID, threadID)
						return a.retryWithFreshThread(ctx, conversationID, message, "empty_response", onProgress)
					}
					return "", fmt.Errorf("agent returned empty response")
				}
				return result, nil
			}
		}
	}
}

// refreshCodexRuntimeAfterUsageLimit 在额度错误后的下一次请求前切换到当前本机 Codex 登录态。
func (a *ACPAgent) refreshCodexRuntimeAfterUsageLimit(ctx context.Context, conversationID string) error {
	if !a.takeCodexUsageLimitRefresh(conversationID) {
		return nil
	}
	oldThreadID := a.clearCodexThread(conversationID)
	log.Printf("[acp] refreshing codex runtime after usage limit (conversation=%s, oldThread=%s)", conversationID, oldThreadID)
	if a.rpcCall != nil {
		return nil
	}
	a.Stop()
	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("refresh codex runtime after usage limit: %w", err)
	}
	return nil
}

// markCodexUsageLimitRefresh 标记下一次请求需要刷新 runtime，等待用户手动切换账号。
func (a *ACPAgent) markCodexUsageLimitRefresh(conversationID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usageLimitRefreshOnNextTurn[conversationID] = true
}

// takeCodexUsageLimitRefresh 取出并清除额度错误后的刷新标记。
func (a *ACPAgent) takeCodexUsageLimitRefresh(conversationID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	shouldRefresh := a.usageLimitRefreshOnNextTurn[conversationID]
	delete(a.usageLimitRefreshOnNextTurn, conversationID)
	return shouldRefresh
}

func (a *ACPAgent) retryWithFreshThread(ctx context.Context, conversationID string, message string, reason string, onProgress func(delta string)) (string, error) {
	oldThreadID := a.clearCodexThread(conversationID)

	log.Printf("[acp] cleared stale thread mapping (conversation=%s, oldThread=%s, reason=%s), creating fresh thread", conversationID, oldThreadID, reason)
	retryMessage := message
	if hydrated, ok := a.buildRehydratePrompt(conversationID, message); ok {
		retryMessage = hydrated
		log.Printf("[acp] using local context rehydrate prompt for fresh thread (conversation=%s)", conversationID)
	}

	result, err := a.chatCodexAppServerWithRetry(ctx, conversationID, retryMessage, false, onProgress)
	if err != nil {
		return "", fmt.Errorf("retry with fresh thread failed: %w", err)
	}
	return result, nil
}

// invalidateCodexRuntime 在账号态异常时丢弃旧进程，避免后续请求继续使用失效登录态。
func (a *ACPAgent) invalidateCodexRuntime(conversationID string, reason string) {
	oldThreadID := a.clearCodexThread(conversationID)
	log.Printf("[acp] invalidating codex runtime (conversation=%s, oldThread=%s, reason=%s)", conversationID, oldThreadID, reason)
	a.Stop()
}

// clearCodexThread 只清理远端 thread 映射，保留本地历史用于后续恢复上下文。
func (a *ACPAgent) clearCodexThread(conversationID string) string {
	a.mu.Lock()
	oldThreadID := a.threads[conversationID]
	delete(a.threads, conversationID)
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	a.persistState()
	return oldThreadID
}

func (a *ACPAgent) recordConversationExchange(conversationID, userText, assistantText string) {
	if conversationID == "" {
		return
	}

	userText = strings.TrimSpace(userText)
	assistantText = strings.TrimSpace(assistantText)
	if userText == "" || assistantText == "" {
		return
	}

	a.mu.Lock()
	h := append(a.history[conversationID],
		acpHistoryMessage{Role: "user", Text: userText},
		acpHistoryMessage{Role: "assistant", Text: assistantText},
	)
	if len(h) > acpMaxHistoryMessages {
		h = h[len(h)-acpMaxHistoryMessages:]
	}
	a.history[conversationID] = h
	a.mu.Unlock()
	a.persistState()
}

func (a *ACPAgent) buildRehydratePrompt(conversationID, currentMessage string) (string, bool) {
	a.mu.Lock()
	history := append([]acpHistoryMessage(nil), a.history[conversationID]...)
	a.mu.Unlock()

	if len(history) == 0 {
		return "", false
	}

	render := func(from int) string {
		var b strings.Builder
		b.WriteString("Context from the previous conversation (auto-restored after thread/account switch):\n")
		for _, msg := range history[from:] {
			role := "User"
			if msg.Role == "assistant" {
				role = "Assistant"
			}
			b.WriteString(role)
			b.WriteString(": ")
			b.WriteString(msg.Text)
			b.WriteString("\n")
		}
		b.WriteString("\nCurrent user message:\n")
		b.WriteString(currentMessage)
		b.WriteString("\n\nPlease continue the conversation using the restored context.")
		return b.String()
	}

	start := 0
	prompt := render(start)
	for len(prompt) > acpMaxRehydratePromptChars && start < len(history)-1 {
		start++
		prompt = render(start)
	}
	return prompt, true
}

func isMissingThreadError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	hasEntity := strings.Contains(msg, "thread") || strings.Contains(msg, "conversation") || strings.Contains(msg, "session")
	hasMissing := strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no conversation found") ||
		strings.Contains(msg, "unknown thread") ||
		strings.Contains(msg, "unknown session")
	return hasEntity && hasMissing
}

func defaultACPStateFile(command string, args []string, cwd string, protocol string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".weclaw", "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[acp] failed to create state dir: %v", err)
		return ""
	}
	key := strings.Join([]string{
		strings.ToLower(command),
		strings.Join(args, "\x00"),
		cwd,
		protocol,
	}, "\x00")
	sum := sha1.Sum([]byte(key))
	return filepath.Join(dir, "acp-"+hex.EncodeToString(sum[:])+".json")
}

func (a *ACPAgent) loadState() {
	if a.stateFile == "" {
		return
	}

	data, err := os.ReadFile(a.stateFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[acp] failed to read state file %s: %v", a.stateFile, err)
		}
		return
	}

	var state acpPersistedState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("[acp] failed to parse state file %s: %v", a.stateFile, err)
		return
	}

	loadedSessions := 0
	loadedThreads := 0
	loadedHistory := 0

	a.mu.Lock()
	for conversationID, sessionID := range state.Sessions {
		if conversationID == "" || sessionID == "" {
			continue
		}
		a.sessions[conversationID] = sessionID
		loadedSessions++
	}
	for conversationID, threadID := range state.Threads {
		if conversationID == "" || threadID == "" {
			continue
		}
		a.threads[conversationID] = threadID
		a.resumeOnFirstUse[conversationID] = true
		loadedThreads++
	}
	for conversationID, messages := range state.History {
		if conversationID == "" || len(messages) == 0 {
			continue
		}
		normalized := make([]acpHistoryMessage, 0, len(messages))
		for _, msg := range messages {
			role := strings.TrimSpace(strings.ToLower(msg.Role))
			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}
			if role != "user" && role != "assistant" {
				continue
			}
			normalized = append(normalized, acpHistoryMessage{
				Role: role,
				Text: text,
			})
		}
		if len(normalized) == 0 {
			continue
		}
		if len(normalized) > acpMaxHistoryMessages {
			normalized = normalized[len(normalized)-acpMaxHistoryMessages:]
		}
		a.history[conversationID] = normalized
		loadedHistory++
	}
	a.mu.Unlock()

	if loadedSessions > 0 || loadedThreads > 0 || loadedHistory > 0 {
		log.Printf("[acp] restored state (sessions=%d, threads=%d, history=%d, file=%s)", loadedSessions, loadedThreads, loadedHistory, a.stateFile)
	}
}

func (a *ACPAgent) persistState() {
	if a.stateFile == "" {
		return
	}

	a.mu.Lock()
	state := acpPersistedState{
		Version:  acpPersistedStateVersion,
		Protocol: a.protocol,
		Sessions: make(map[string]string, len(a.sessions)),
		Threads:  make(map[string]string, len(a.threads)),
		History:  make(map[string][]acpHistoryMessage, len(a.history)),
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}
	for k, v := range a.sessions {
		state.Sessions[k] = v
	}
	for k, v := range a.threads {
		state.Threads[k] = v
	}
	for conversationID, messages := range a.history {
		if len(messages) == 0 {
			continue
		}
		copied := make([]acpHistoryMessage, len(messages))
		copy(copied, messages)
		state.History[conversationID] = copied
	}
	stateFile := a.stateFile
	a.mu.Unlock()

	dir := filepath.Dir(stateFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[acp] failed to create state dir %s: %v", dir, err)
		return
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("[acp] failed to marshal state: %v", err)
		return
	}

	tmpFile := stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		log.Printf("[acp] failed to write state tmp file %s: %v", tmpFile, err)
		return
	}
	if err := os.Rename(tmpFile, stateFile); err != nil {
		log.Printf("[acp] failed to move state file into place %s: %v", stateFile, err)
		_ = os.Remove(tmpFile)
		return
	}
}

func (a *ACPAgent) rpc(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if a.rpcCall != nil {
		return a.rpcCall(ctx, method, params)
	}
	return a.call(ctx, method, params)
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (a *ACPAgent) notify(method string, params interface{}) error {
	msg := struct {
		JSONRPC string      `json:"jsonrpc"`
		Method  string      `json:"method"`
		Params  interface{} `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	err = a.writeJSONLine(data)
	return err
}

// writeJSONLine 在写入 ACP stdin 前检查 runtime 状态，避免读循环退出后 nil stdin 触发 panic。
func (a *ACPAgent) writeJSONLine(data []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdin == nil {
		return fmt.Errorf("ACP runtime is not running")
	}
	_, err := fmt.Fprintf(a.stdin, "%s\n", data)
	return err
}

// call sends a JSON-RPC request and waits for the response.
func (a *ACPAgent) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := a.nextID.Add(1)

	ch := make(chan *rpcResponse, 1)
	a.pendingMu.Lock()
	a.pending[id] = ch
	a.pendingMu.Unlock()

	defer func() {
		a.pendingMu.Lock()
		delete(a.pending, id)
		a.pendingMu.Unlock()
	}()

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	err = a.writeJSONLine(data)
	if err != nil {
		return nil, fmt.Errorf("write to stdin: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			msg := formatRPCErrorMessage(resp.Error, a.stderr)
			return nil, fmt.Errorf("agent error: %s", msg)
		}
		return resp.Result, nil
	}
}

// formatRPCErrorMessage 保留 JSON-RPC error 的结构化信息，并避免 stderr 的残缺 JSON 片段覆盖主错误。
func formatRPCErrorMessage(rpcErr *rpcError, stderr *acpStderrWriter) string {
	var parts []string
	if rpcErr != nil {
		if message := strings.TrimSpace(rpcErr.Message); message != "" {
			parts = append(parts, message)
		}
		if data := formatRPCErrorData(rpcErr.Data); data != "" {
			parts = append(parts, data)
		}
	}
	if stderr != nil {
		if detail := normalizeStderrDetail(stderr.LastError()); detail != "" {
			parts = append(parts, detail)
		}
	}
	if len(parts) == 0 {
		return "未知 Agent 错误"
	}
	return strings.Join(dedupeStrings(parts), "；")
}

func formatRPCErrorData(data json.RawMessage) string {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" || text == "{}" {
		return ""
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	var asObject map[string]interface{}
	if err := json.Unmarshal(data, &asObject); err == nil {
		return flattenJSONMap(asObject)
	}
	return normalizeStderrDetail(text)
}

func flattenJSONMap(values map[string]interface{}) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprint(values[key]))
		if value != "" && value != "<nil>" {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, ", ")
}

func normalizeStderrDetail(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || text == "}" || text == "]" || text == "{" || text == "[" {
		return ""
	}
	return text
}

func dedupeStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// readLoop reads NDJSON lines from stdout and dispatches to pending requests or notification channels.
func (a *ACPAgent) readLoop() {
	a.mu.Lock()
	scanner := a.scanner
	a.mu.Unlock()
	if scanner == nil {
		return
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg rpcResponse
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("[acp] failed to parse message: %v", err)
			continue
		}

		// Response to a request we made (has id, no method)
		if msg.ID != nil && msg.Method == "" {
			a.pendingMu.Lock()
			ch, ok := a.pending[*msg.ID]
			a.pendingMu.Unlock()
			if ok {
				ch <- &msg
			}
			continue
		}

		// 处理 agent 主动发出的请求或通知。
		switch msg.Method {
		case "session/update":
			a.handleSessionUpdate(msg.Params)

		case "session/request_permission":
			// 旧 ACP 权限请求会复用统一审批处理链路。
			a.handlePermissionRequest(line)

		// Codex app-server 事件，不同版本会发出不同 method。
		case "codex/event/agent_message_delta":
			a.handleCodexDelta(msg.Params)
		case "item/agentMessage/delta":
			a.handleCodexItemDelta(msg.Params)
		case "item/started":
			a.handleCodexItemStarted(msg.Params)
		case "turn/started", "turn/completed":
			a.handleCodexTurnEvent(msg.Method, msg.Params)
		case "error":
			a.handleCodexError(msg.Params)
		case "item/completed":
			a.handleCodexItemCompleted(msg.Params)
		case "codex/event/agent_message", "codex/event/task_complete",
			"codex/event/item_completed", "codex/event/token_count",
			"thread/tokenUsage/updated",
			"account/rateLimits/updated", "thread/status/changed":
			// 这些是已知状态事件，当前桥接层不需要额外处理。
		case "turn/approval/request",
			"item/fileChange/requestApproval",
			"item/commandExecution/requestApproval":
			a.handlePermissionRequest(line)

		default:
			if msg.Method != "" {
				log.Printf("[acp] unhandled method: %s (raw: %.200s)", msg.Method, line)
			}
		}
	}

	exitReason := "ACP runtime exited"
	if err := scanner.Err(); err != nil {
		exitReason = fmt.Sprintf("ACP runtime read error: %v", err)
		log.Printf("[acp] read loop error: %v", err)
	}
	a.mu.Lock()
	currentScanner := a.scanner == scanner
	if a.scanner == scanner {
		a.started = false
		a.stdin = nil
		a.cmd = nil
		a.scanner = nil
	}
	a.mu.Unlock()
	if currentScanner {
		a.failRuntimeWaiters(exitReason)
	}
	log.Println("[acp] read loop ended")
}

func (a *ACPAgent) failRuntimeWaiters(reason string) {
	a.failPendingRequests(reason)
	a.failActiveTurns(reason)
}

func (a *ACPAgent) failPendingRequests(reason string) {
	resp := &rpcResponse{
		Error: &rpcError{Code: -32000, Message: reason},
	}

	a.pendingMu.Lock()
	channels := make([]chan *rpcResponse, 0, len(a.pending))
	for id, ch := range a.pending {
		delete(a.pending, id)
		channels = append(channels, ch)
	}
	a.pendingMu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- resp:
		default:
		}
	}
}

func (a *ACPAgent) failActiveTurns(reason string) {
	evt := &codexTurnEvent{Kind: "error", Text: reason}
	a.notifyMu.Lock()
	channels := make([]chan *codexTurnEvent, 0, len(a.turnCh))
	for _, ch := range a.turnCh {
		channels = append(channels, ch)
	}
	a.notifyMu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (a *ACPAgent) handleSessionUpdate(params json.RawMessage) {
	var p sessionUpdateParams
	if err := json.Unmarshal(params, &p); err != nil {
		log.Printf("[acp] failed to parse session/update: %v (raw: %s)", err, string(params))
		return
	}

	// Only log non-streaming events (skip chunks to reduce noise)
	switch p.Update.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk":
		// skip — too noisy
	default:
		log.Printf("[acp] session/update (session=%s, type=%s)", p.SessionID, p.Update.SessionUpdate)
	}

	a.notifyMu.Lock()
	ch, ok := a.notifyCh[p.SessionID]
	a.notifyMu.Unlock()

	if ok {
		select {
		case ch <- &p.Update:
		default:
			log.Printf("[acp] notification channel full, dropping update (session=%s)", p.SessionID)
		}
	}
}

func (a *ACPAgent) handleCodexDelta(params json.RawMessage) {
	var p struct {
		Msg struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"msg"`
		ConversationID string `json:"conversationId"`
		ThreadID       string `json:"threadId"` // some versions use threadId
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	// Try conversationId first (codex uses this), fallback to threadId
	key := p.ConversationID
	if key == "" {
		key = p.ThreadID
	}

	delta := p.Msg.Delta
	if delta == "" {
		return
	}

	a.dispatchToTurnCh(key, &codexTurnEvent{Delta: delta})
}

// handleCodexItemDelta handles "item/agentMessage/delta" events.
// These contain incremental text deltas for the agent's response.
func (a *ACPAgent) handleCodexItemDelta(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if p.Delta == "" {
		return
	}

	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{ItemID: p.ItemID, Delta: p.Delta})
}

// handleCodexItemStarted handles "item/started" events.
// When type=agentMessage, extracts text from content array.
func (a *ACPAgent) handleCodexItemStarted(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Item     struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if p.Item.Type != "agentMessage" {
		return
	}

	for _, c := range p.Item.Content {
		if c.Type == "text" && c.Text != "" {
			a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{ItemID: p.Item.ID, Text: c.Text})
		}
	}
}

// handleCodexItemCompleted 将 completed 文本作为兜底最终文本来源。
func (a *ACPAgent) handleCodexItemCompleted(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Item     struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	if p.Item.Type != "agentMessage" {
		return
	}
	for _, c := range p.Item.Content {
		if c.Type == "text" && c.Text != "" {
			a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "item_completed", ItemID: p.Item.ID, Text: c.Text})
		}
	}
}

// handleCodexTurnEvent handles "turn/started" and "turn/completed" notifications.
func (a *ACPAgent) handleCodexTurnEvent(method string, params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	if method == "turn/completed" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: "completed"})
	}
}

func (a *ACPAgent) handleCodexError(params json.RawMessage) {
	if isRecoverableCodexTransportText(string(params)) {
		log.Printf("[acp] ignoring recoverable codex transport error: %.200s", string(params))
		return
	}
	text := formatCodexError(params)
	if text == "" && a.stderr != nil {
		stderrText := a.stderr.LastError()
		if isRecoverableCodexTransportText(stderrText) {
			log.Printf("[acp] ignoring recoverable codex stderr transport error: %.200s", stderrText)
			return
		}
		text = formatCodexStderrError(stderrText)
	}
	if text == "" {
		text = "Codex 返回未知错误"
	}
	a.dispatchToTurnCh("", &codexTurnEvent{Kind: "error", Text: text})
}

func formatCodexError(params json.RawMessage) string {
	var p struct {
		Error struct {
			Message        string `json:"message"`
			CodexErrorInfo string `json:"codexErrorInfo"`
			Code           string `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
		Code    string `json:"code"`
		Detail  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}

	message := firstNonEmpty(p.Error.Message, p.Message, p.Detail.Message)
	info := firstNonEmpty(p.Error.CodexErrorInfo, p.Error.Code, p.Code, p.Detail.Code)
	if isRecoverableCodexTransportText(message) || isRecoverableCodexTransportText(info) {
		return ""
	}
	if info == "deactivated_workspace" {
		return joinCodexErrorParts("Codex 工作区不可用", message, info)
	}
	if info == "usageLimitExceeded" && message != "" {
		return "Codex 账号额度已用完：" + message + " (" + info + ")"
	}
	if info != "" && message != "" {
		return message + " (" + info + ")"
	}
	if message != "" {
		return message
	}
	return info
}

// formatCodexStderrError 从 Codex stderr 中提取账号态错误，补足空泛的 error 事件。
func formatCodexStderrError(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "deactivated_workspace") {
		return joinCodexErrorParts("Codex 工作区不可用", text, "deactivated_workspace")
	}
	if strings.Contains(lower, "402 payment required") {
		return joinCodexErrorParts("Codex 认证或工作区不可用", text, "")
	}
	return text
}

// isRecoverableCodexTransportText 判断 Codex responses WebSocket 失败是否属于可恢复传输噪声。
func isRecoverableCodexTransportText(text string) bool {
	lower := strings.ToLower(text)
	hasWebSocketSignal := strings.Contains(lower, "responses_websocket") ||
		strings.Contains(lower, "websocket") ||
		strings.Contains(lower, "ws://")
	hasForbiddenSignal := strings.Contains(lower, "403 forbidden")
	hasRecoverSignal := strings.Contains(lower, "falling back from websockets to https transport") ||
		strings.Contains(lower, "failed to connect to websocket") ||
		strings.Contains(lower, "reconnecting")
	return hasWebSocketSignal && hasForbiddenSignal && hasRecoverSignal
}

// isCodexAuthStateError 判断错误是否来自登录态或工作区状态；额度耗尽不能刷新进程。
func isCodexAuthStateError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "deactivated_workspace") ||
		(strings.Contains(lower, "402 payment required") && !strings.Contains(lower, "usagelimitexceeded"))
}

// isCodexUsageLimitError 判断 Codex 当前账号额度是否耗尽。
func isCodexUsageLimitError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "usagelimitexceeded") ||
		strings.Contains(lower, "you've hit your usage limit")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func joinCodexErrorParts(title string, message string, code string) string {
	var parts []string
	if title != "" {
		parts = append(parts, title)
	}
	if message != "" {
		parts = append(parts, message)
	}
	if code != "" {
		parts = append(parts, "("+code+")")
	}
	return strings.Join(parts, "：")
}

// dispatchToTurnCh sends an event to the turn channel for a thread.
func (a *ACPAgent) dispatchToTurnCh(threadID string, evt *codexTurnEvent) bool {
	a.notifyMu.Lock()
	ch, ok := a.turnCh[threadID]
	if !ok {
		// 只有一个活跃 turn 时才 fallback，避免多会话事件串到错误用户。
		if len(a.turnCh) == 1 {
			for _, c := range a.turnCh {
				ch = c
				ok = true
				break
			}
		} else if len(a.turnCh) > 1 {
			log.Printf("[acp] dropping turn event without routable thread (thread=%q, activeTurns=%d, kind=%s)", threadID, len(a.turnCh), evt.Kind)
		}
	}
	a.notifyMu.Unlock()

	if ok {
		select {
		case ch <- evt:
			return true
		default:
		}
	}
	return false
}

func (a *ACPAgent) handlePermissionRequest(raw string) {
	var req struct {
		ID     json.RawMessage         `json:"id"`
		Method string                  `json:"method"`
		Params permissionRequestParams `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		log.Printf("[acp] failed to parse permission request: %v", err)
		return
	}

	responseFormat := permissionResponseFormatForMethod(req.Method)
	approval := &codexApprovalRequest{
		ID:             req.ID,
		ResponseFormat: responseFormat,
		Request: ApprovalRequest{
			ToolCall: permissionToolCall(req.Params),
			Options:  approvalOptionsFromPermission(req.Params),
		},
	}
	if a.dispatchToTurnCh(req.Params.ThreadID, &codexTurnEvent{Kind: "approval_request", Approval: approval}) {
		return
	}
	optionID := selectPermissionOption(req.Params, "deny")
	if err := a.respondPermissionRequest(req.ID, optionID, responseFormat); err != nil {
		log.Printf("[acp] failed to deny unroutable permission request: %v", err)
	}
}

// UnmarshalJSON 兼容 Codex command 审批字段的新旧形态：字符串数组或单个命令字符串。
func (c *permissionCommand) UnmarshalJSON(data []byte) error {
	var parts []string
	if err := json.Unmarshal(data, &parts); err == nil {
		*c = permissionCommand(parts)
		return nil
	}
	var command string
	if err := json.Unmarshal(data, &command); err != nil {
		return err
	}
	command = strings.TrimSpace(command)
	if command == "" {
		*c = nil
		return nil
	}
	*c = permissionCommand{command}
	return nil
}

// UnmarshalJSON 兼容 Codex availableDecisions 字段：字符串数组或带 decision 的对象数组。
func (d *permissionDecisions) UnmarshalJSON(data []byte) error {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &rawItems); err == nil {
		*d = parsePermissionDecisionItems(rawItems)
		return nil
	}
	decision, ok, err := parsePermissionDecisionValue(data)
	if err != nil {
		return err
	}
	if !ok {
		*d = nil
		return nil
	}
	*d = permissionDecisions{decision}
	return nil
}

// parsePermissionDecisionItems 逐项提取新版审批 decision，跳过空对象。
func parsePermissionDecisionItems(items []json.RawMessage) permissionDecisions {
	decisions := make(permissionDecisions, 0, len(items))
	for _, item := range items {
		if decision, ok, _ := parsePermissionDecisionValue(item); ok {
			decisions = append(decisions, decision)
		}
	}
	return decisions
}

// parsePermissionDecisionValue 从字符串或对象中取出实际要回传给 Codex 的 decision。
func parsePermissionDecisionValue(data json.RawMessage) (string, bool, error) {
	var decision string
	if err := json.Unmarshal(data, &decision); err == nil {
		return strings.TrimSpace(decision), strings.TrimSpace(decision) != "", nil
	}
	var object struct {
		Decision string `json:"decision"`
		ID       string `json:"id"`
		OptionID string `json:"optionId"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return "", false, err
	}
	decision = firstNonEmptyString(object.Decision, object.ID, object.OptionID, object.Value)
	return decision, decision != "", nil
}

// firstNonEmptyString 返回第一个非空字符串，用于兼容不同对象字段名。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (a *ACPAgent) resolvePermissionOption(ctx context.Context, req ApprovalRequest) string {
	fallback := selectApprovalOption(req.Options, defaultDenyDecision(req.Options))
	handler := approvalHandlerFromContext(ctx)
	if handler == nil {
		return fallback
	}
	optionID, err := handler(ctx, req)
	if err != nil {
		log.Printf("[acp] approval handler failed, denying request: %v", err)
		return fallback
	}
	if isApprovalOption(req.Options, optionID) {
		return optionID
	}
	log.Printf("[acp] approval handler returned unknown option %q, denying request", optionID)
	return fallback
}

func (a *ACPAgent) respondPermissionRequest(id json.RawMessage, optionID string, responseFormat permissionResponseFormat) error {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if responseFormat == permissionResponseDecision {
		resp["result"] = map[string]interface{}{
			"decision": optionID,
		}
	} else {
		resp["result"] = map[string]interface{}{
			"outcome": map[string]interface{}{
				"outcome":  "selected",
				"optionId": optionID,
			},
		}
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal permission response: %w", err)
	}
	return a.writeJSONLine(data)
}

// permissionResponseFormatForMethod 区分旧 ACP 和新版 Codex item 审批响应结构。
func permissionResponseFormatForMethod(method string) permissionResponseFormat {
	switch method {
	case "item/fileChange/requestApproval", "item/commandExecution/requestApproval":
		return permissionResponseDecision
	default:
		return permissionResponseOutcome
	}
}

// permissionToolCall 为新版审批请求补出可读命令，避免飞书按钮只显示泛化提示。
func permissionToolCall(params permissionRequestParams) json.RawMessage {
	if len(params.ToolCall) > 0 && string(params.ToolCall) != "null" {
		return params.ToolCall
	}
	tool := map[string]interface{}{}
	if len(params.Command) > 0 {
		tool["cmd"] = strings.Join(params.Command, " ")
	}
	if strings.TrimSpace(params.Cwd) != "" {
		tool["cwd"] = params.Cwd
	}
	if len(tool) == 0 {
		return nil
	}
	data, err := json.Marshal(tool)
	if err != nil {
		return nil
	}
	return data
}

// approvalOptionsFromPermission 统一旧 options 和新版 availableDecisions。
func approvalOptionsFromPermission(params permissionRequestParams) []ApprovalOption {
	result := make([]ApprovalOption, 0, len(params.Options)+len(params.AvailableDecisions))
	for _, opt := range params.Options {
		result = append(result, ApprovalOption{ID: opt.OptionID, Name: opt.Name, Kind: opt.Kind})
	}
	for _, decision := range params.AvailableDecisions {
		decision = strings.TrimSpace(decision)
		if decision == "" {
			continue
		}
		result = append(result, ApprovalOption{ID: decision, Name: decision, Kind: approvalKindFromDecision(decision)})
	}
	return result
}

// approvalKindFromDecision 把新版 decision 字符串映射到通用允许/拒绝类型。
func approvalKindFromDecision(decision string) string {
	lower := strings.ToLower(strings.TrimSpace(decision))
	switch {
	case strings.Contains(lower, "cancel"), strings.Contains(lower, "deny"), strings.Contains(lower, "reject"):
		return "deny"
	case strings.Contains(lower, "accept"), strings.Contains(lower, "allow"), strings.Contains(lower, "approve"):
		return "allow"
	default:
		return lower
	}
}

func (a *ACPAgent) approvalPolicyForContext(ctx context.Context) string {
	if policy := strings.TrimSpace(a.approvalPolicy); policy != "" {
		return policy
	}
	return approvalPolicyForContext(ctx)
}

func (a *ACPAgent) sandboxModeForCodex() string {
	mode := strings.TrimSpace(a.sandboxMode)
	if mode == "" {
		return "danger-full-access"
	}
	switch strings.ToLower(mode) {
	case "readonly", "read_only", "read-only":
		return "read-only"
	case "workspacewrite", "workspace_write", "workspace-write":
		return "workspace-write"
	case "dangerfullaccess", "danger_full_access", "danger-full-access":
		return "danger-full-access"
	default:
		return mode
	}
}

func (a *ACPAgent) sandboxPolicyTypeForCodex() string {
	switch a.sandboxModeForCodex() {
	case "read-only":
		return "readOnly"
	case "workspace-write":
		return "workspaceWrite"
	case "danger-full-access":
		return "dangerFullAccess"
	default:
		return strings.TrimSpace(a.sandboxMode)
	}
}

// selectPermissionOption 在无法路由给用户时选择保守 fallback，优先拒绝。
func selectPermissionOption(params permissionRequestParams, preferredKind string) string {
	for _, opt := range params.Options {
		if opt.Kind == preferredKind && strings.TrimSpace(opt.OptionID) != "" {
			return opt.OptionID
		}
	}
	for _, decision := range params.AvailableDecisions {
		if approvalKindFromDecision(decision) == preferredKind && strings.TrimSpace(decision) != "" {
			return strings.TrimSpace(decision)
		}
	}
	for _, opt := range params.Options {
		if opt.Kind != "allow" && strings.TrimSpace(opt.OptionID) != "" {
			return opt.OptionID
		}
	}
	for _, decision := range params.AvailableDecisions {
		if approvalKindFromDecision(decision) != "allow" && strings.TrimSpace(decision) != "" {
			return strings.TrimSpace(decision)
		}
	}
	if len(params.Options) > 0 {
		return params.Options[0].OptionID
	}
	if len(params.AvailableDecisions) > 0 {
		return strings.TrimSpace(params.AvailableDecisions[0])
	}
	return preferredKind
}

func selectApprovalOption(options []ApprovalOption, preferredKind string) string {
	for _, opt := range options {
		if opt.Kind == preferredKind && strings.TrimSpace(opt.ID) != "" {
			return opt.ID
		}
	}
	for _, opt := range options {
		if opt.Kind != "allow" && strings.TrimSpace(opt.ID) != "" {
			return opt.ID
		}
	}
	if len(options) > 0 {
		return options[0].ID
	}
	return preferredKind
}

// defaultDenyDecision 在 Codex 新版审批请求缺少 options 时返回协议认可的拒绝值。
func defaultDenyDecision(options []ApprovalOption) string {
	for _, opt := range options {
		if strings.EqualFold(strings.TrimSpace(opt.ID), "cancel") {
			return "cancel"
		}
	}
	return "decline"
}

func isApprovalOption(options []ApprovalOption, optionID string) bool {
	optionID = strings.TrimSpace(optionID)
	if optionID == "" {
		return false
	}
	for _, opt := range options {
		if opt.ID == optionID {
			return true
		}
	}
	return false
}

// Info returns metadata about this agent.
func (a *ACPAgent) Info() AgentInfo {
	info := AgentInfo{
		Name:    a.command,
		Type:    "acp",
		Model:   a.model,
		Effort:  a.effort,
		Command: a.command,
	}
	a.mu.Lock()
	if a.cmd != nil && a.cmd.Process != nil {
		info.PID = a.cmd.Process.Pid
	}
	a.mu.Unlock()
	return info
}

func extractChunkText(update *sessionUpdate) string {
	// The content field in agent_message_chunk can be a text content block
	if update.Text != "" {
		return update.Text
	}

	// Try to extract from content JSON
	if update.Content != nil {
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(update.Content, &content); err == nil && content.Text != "" {
			return content.Text
		}
	}

	return ""
}

// extractPromptResultText tries to extract text from the session/prompt response.
// Some ACP agents include response content in the result alongside stopReason.
func extractPromptResultText(result json.RawMessage) string {
	if result == nil {
		return ""
	}

	// Try to extract content array from result
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// Some agents use a flat text field
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return ""
	}

	if r.Text != "" {
		return r.Text
	}

	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "")
}

// acpStderrWriter forwards the ACP subprocess stderr to the application log
// and captures the last meaningful error line.
type acpStderrWriter struct {
	prefix string
	mu     sync.Mutex
	last   string // last non-empty, non-traceback line
}

func (w *acpStderrWriter) Write(p []byte) (int, error) {
	lines := strings.Split(strings.TrimRight(string(p), "\n"), "\n")
	w.mu.Lock()
	for _, line := range lines {
		if line != "" {
			log.Printf("%s %s", w.prefix, line)
			// Capture lines that look like actual error messages (not traceback frames)
			if !strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "Traceback") && !strings.HasPrefix(line, "...") {
				w.last = line
			}
		}
	}
	w.mu.Unlock()
	return len(p), nil
}

// LastError returns the last captured error line and resets it.
func (w *acpStderrWriter) LastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.last
	w.last = ""
	return s
}
