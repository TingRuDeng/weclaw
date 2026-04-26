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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ACPAgent communicates with ACP-compatible agents (claude-agent-acp, codex-acp, cursor agent, etc.) via stdio JSON-RPC 2.0.
type ACPAgent struct {
	command      string
	args         []string
	model        string
	systemPrompt string
	cwd          string
	env          map[string]string
	protocol     string // "legacy_acp" or "codex_app_server"

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
	resumeOnFirstUse map[string]bool // conversationID -> resume needed
	stateFile        string          // optional persisted state file path
	history          map[string][]acpHistoryMessage

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
	Command      string   // path to ACP agent binary (claude-agent-acp, codex-acp, cursor agent, etc.)
	Args         []string // extra args for command (e.g. ["acp"] for cursor)
	Model        string
	SystemPrompt string
	Cwd          string            // working directory
	Env          map[string]string // extra environment variables
	StateFile    string            // optional persisted mapping file path
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
	Code    int    `json:"code"`
	Message string `json:"message"`
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
	ToolCall json.RawMessage    `json:"toolCall"`
	Options  []permissionOption `json:"options"`
}

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

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
	Cwd            string           `json:"cwd,omitempty"`
}

type codexUserInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type codexTurnEvent struct {
	Kind  string
	Delta string
	Text  string
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
		command:          cfg.Command,
		args:             cfg.Args,
		model:            cfg.Model,
		systemPrompt:     cfg.SystemPrompt,
		cwd:              cfg.Cwd,
		env:              cfg.Env,
		protocol:         protocol,
		sessions:         make(map[string]string),
		threads:          make(map[string]string),
		resumeOnFirstUse: make(map[string]bool),
		stateFile:        stateFile,
		history:          make(map[string][]acpHistoryMessage),
		pending:          make(map[int64]chan *rpcResponse),
		notifyCh:         make(map[string]chan *sessionUpdate),
		turnCh:           make(map[string]chan *codexTurnEvent),
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

	a.scanner = bufio.NewScanner(stdout)
	a.scanner.Buffer(make([]byte, 0, 4*1024*1024), 4*1024*1024) // 4MB
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
		a.mu.Unlock()
		a.stdin.Close()
		a.cmd.Process.Kill()
		a.cmd.Wait()
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

func codexInitializeParams() map[string]interface{} {
	return map[string]interface{}{
		"clientInfo": map[string]string{"name": "weclaw", "version": "0.3.0"},
		"capabilities": map[string]interface{}{
			"experimentalApi": true,
		},
	}
}

// Stop terminates the subprocess.
func (a *ACPAgent) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.started {
		return
	}
	a.stdin.Close()
	a.cmd.Process.Kill()
	a.cmd.Wait()
	a.started = false
}

// SetCwd changes the working directory for subsequent sessions.
func (a *ACPAgent) SetCwd(cwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cwd = cwd
}

// ResetSession clears the existing session for the given conversationID and
// immediately creates a new one, returning the new session ID.
func (a *ACPAgent) ResetSession(ctx context.Context, conversationID string) (string, error) {
	if a.protocol == protocolCodexAppServer {
		a.mu.Lock()
		delete(a.threads, conversationID)
		delete(a.resumeOnFirstUse, conversationID)
		delete(a.history, conversationID)
		a.mu.Unlock()
		a.persistState()
		log.Printf("[acp] thread reset (conversation=%s), creating new thread", conversationID)

		threadID, _, err := a.getOrCreateThread(ctx, conversationID)
		if err != nil {
			return "", fmt.Errorf("create new thread: %w", err)
		}
		return threadID, nil
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

// Chat sends a message and returns the full response.
func (a *ACPAgent) Chat(ctx context.Context, conversationID string, message string) (string, error) {
	return a.chat(ctx, conversationID, message, nil)
}

// ChatWithProgress sends a message and emits incremental deltas during generation.
func (a *ACPAgent) ChatWithProgress(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	return a.chat(ctx, conversationID, message, onProgress)
}

func (a *ACPAgent) chat(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	if !a.started {
		if err := a.Start(ctx); err != nil {
			return "", err
		}
	}

	// Route to codex app-server protocol if applicable
	if a.protocol == protocolCodexAppServer {
		return a.chatCodexAppServer(ctx, conversationID, message, onProgress)
	}

	// Get or create session
	sessionID, isNew, err := a.getOrCreateSession(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("session error: %w", err)
	}

	pid := a.cmd.Process.Pid
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

func (a *ACPAgent) getOrCreateSession(ctx context.Context, conversationID string) (string, bool, error) {
	a.mu.Lock()
	sid, exists := a.sessions[conversationID]
	a.mu.Unlock()

	if exists {
		return sid, false, nil
	}

	result, err := a.rpc(ctx, "session/new", newSessionParams{
		Cwd:        a.cwd,
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
			if err := a.resumeThread(ctx, tid); err != nil {
				log.Printf("[acp] failed to resume restored thread (conversation=%s, thread=%s): %v", conversationID, tid, err)
			} else {
				log.Printf("[acp] restored thread resumed (conversation=%s, thread=%s)", conversationID, tid)
			}
		}
		return tid, false, nil
	}

	params := map[string]interface{}{
		"approvalPolicy":         "never",
		"cwd":                    a.cwd,
		"sandbox":                "danger-full-access",
		"persistExtendedHistory": true,
	}
	if a.model != "" {
		params["model"] = a.model
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

func (a *ACPAgent) resumeThread(ctx context.Context, threadID string) error {
	if threadID == "" {
		return fmt.Errorf("empty thread id")
	}

	params := map[string]interface{}{
		"threadId":       threadID,
		"approvalPolicy": "never",
		"cwd":            a.cwd,
		"sandbox":        "danger-full-access",
	}
	if a.model != "" {
		params["model"] = a.model
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
				ApprovalPolicy: "never",
				Input:          []codexUserInput{{Type: "text", Text: message}},
				SandboxPolicy:  map[string]interface{}{"type": "dangerFullAccess"},
				Model:          a.model,
				Cwd:            a.cwd,
			})
			return err
		}

		err := startTurn()
		if err != nil && isMissingThreadError(err) {
			log.Printf("[acp] turn/start failed with missing thread, attempting thread/resume (thread=%s): %v", threadID, err)
			if resumeErr := a.resumeThread(ctx, threadID); resumeErr == nil {
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

	// Collect text from events until turn/completed
	var textParts []string
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case evt := <-turnCh:
			if evt.Kind == "error" {
				if allowFreshRetry && !isNew && isMissingThreadError(fmt.Errorf("%s", evt.Text)) {
					log.Printf("[acp] stale thread error detected, retrying with a fresh thread (conversation=%s, oldThread=%s)", conversationID, threadID)
					return a.retryWithFreshThread(ctx, conversationID, message, "stale_thread_error", onProgress)
				}
				return "", fmt.Errorf("turn error: %s", evt.Text)
			}
			if evt.Delta != "" {
				textParts = append(textParts, evt.Delta)
				if onProgress != nil {
					onProgress(evt.Delta)
				}
			}
			if evt.Text != "" {
				textParts = append(textParts, evt.Text)
				if onProgress != nil {
					onProgress(evt.Text)
				}
			}
			if evt.Kind == "completed" {
				result := strings.TrimSpace(strings.Join(textParts, ""))
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

func (a *ACPAgent) retryWithFreshThread(ctx context.Context, conversationID string, message string, reason string, onProgress func(delta string)) (string, error) {
	a.mu.Lock()
	oldThreadID := a.threads[conversationID]
	delete(a.threads, conversationID)
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	a.persistState()

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

	a.mu.Lock()
	_, err = fmt.Fprintf(a.stdin, "%s\n", data)
	a.mu.Unlock()
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

	a.mu.Lock()
	_, err = fmt.Fprintf(a.stdin, "%s\n", data)
	a.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write to stdin: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			msg := resp.Error.Message
			// Enrich with stderr context if available
			if a.stderr != nil {
				if detail := a.stderr.LastError(); detail != "" {
					msg = detail
				}
			}
			return nil, fmt.Errorf("agent error: %s", msg)
		}
		return resp.Result, nil
	}
}

// readLoop reads NDJSON lines from stdout and dispatches to pending requests or notification channels.
func (a *ACPAgent) readLoop() {
	for a.scanner.Scan() {
		line := a.scanner.Text()
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

		// Request from agent or notification
		switch msg.Method {
		case "session/update":
			a.handleSessionUpdate(msg.Params)

		case "session/request_permission":
			// Auto-allow all permissions
			a.handlePermissionRequest(line)

		// Codex app-server events (multiple protocol versions)
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
		case "codex/event/agent_message", "codex/event/task_complete",
			"codex/event/item_completed", "codex/event/token_count",
			"item/completed", "thread/tokenUsage/updated",
			"account/rateLimits/updated", "thread/status/changed":
			// Known events we don't need to act on
		case "turn/approval/request":
			a.handlePermissionRequest(line)

		default:
			if msg.Method != "" {
				log.Printf("[acp] unhandled method: %s (raw: %.200s)", msg.Method, line)
			}
		}
	}

	if err := a.scanner.Err(); err != nil {
		log.Printf("[acp] read loop error: %v", err)
	}
	log.Println("[acp] read loop ended")
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

	// Find the turn channel by thread ID — we need to match against stored threads
	a.notifyMu.Lock()
	ch, ok := a.turnCh[key]
	if !ok {
		// Try matching by iterating all turn channels (codex uses conversationId, not threadId)
		for _, c := range a.turnCh {
			ch = c
			ok = true
			break
		}
	}
	a.notifyMu.Unlock()

	if ok {
		select {
		case ch <- &codexTurnEvent{Delta: delta}:
		default:
		}
	}
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

	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Delta: p.Delta})
}

// handleCodexItemStarted handles "item/started" events.
// When type=agentMessage, extracts text from content array.
func (a *ACPAgent) handleCodexItemStarted(params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Item     struct {
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
			a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Text: c.Text})
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
	text := formatCodexError(params)
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
		} `json:"error"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}

	message := strings.TrimSpace(p.Error.Message)
	info := strings.TrimSpace(p.Error.CodexErrorInfo)
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

// dispatchToTurnCh sends an event to the turn channel for a thread.
func (a *ACPAgent) dispatchToTurnCh(threadID string, evt *codexTurnEvent) {
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
		default:
		}
	}
}

func (a *ACPAgent) handlePermissionRequest(raw string) {
	// Parse the request to get the ID and auto-allow
	var req struct {
		ID     json.RawMessage         `json:"id"`
		Params permissionRequestParams `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		log.Printf("[acp] failed to parse permission request: %v", err)
		return
	}

	// Find the "allow" option
	optionID := "allow"
	for _, opt := range req.Params.Options {
		if opt.Kind == "allow" {
			optionID = opt.OptionID
			break
		}
	}

	// Send response
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result": map[string]interface{}{
			"outcome": map[string]interface{}{
				"outcome":  "selected",
				"optionId": optionID,
			},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("[acp] failed to marshal permission response: %v", err)
		return
	}

	a.mu.Lock()
	fmt.Fprintf(a.stdin, "%s\n", data)
	a.mu.Unlock()

	log.Printf("[acp] auto-allowed permission request")
}

// Info returns metadata about this agent.
func (a *ACPAgent) Info() AgentInfo {
	info := AgentInfo{
		Name:    a.command,
		Type:    "acp",
		Model:   a.model,
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
