package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// CLIAgent invokes a local CLI agent (claude, codex, etc.) via streaming JSON.
type CLIAgent struct {
	name             string
	command          string
	args             []string          // extra args from config
	cwd              string            // working directory
	env              map[string]string // extra environment variables
	model            string
	systemPrompt     string
	mu               sync.Mutex
	sessions         map[string]string // conversationID -> session ID for multi-turn
	conversationCwds map[string]string
}

// CLIAgentConfig holds configuration for a CLI agent.
type CLIAgentConfig struct {
	Name         string            // agent name for logging, e.g. "claude", "codex"
	Command      string            // path to binary
	Args         []string          // extra args (e.g. ["--dangerously-skip-permissions"])
	Cwd          string            // working directory (workspace)
	Env          map[string]string // extra environment variables
	Model        string
	SystemPrompt string
}

// NewCLIAgent creates a new CLI agent.
func NewCLIAgent(cfg CLIAgentConfig) *CLIAgent {
	cwd := cfg.Cwd
	if cwd == "" {
		cwd = defaultWorkspace()
	}
	return &CLIAgent{
		name:             cfg.Name,
		command:          cfg.Command,
		args:             cfg.Args,
		cwd:              cwd,
		env:              cfg.Env,
		model:            cfg.Model,
		systemPrompt:     cfg.SystemPrompt,
		sessions:         make(map[string]string),
		conversationCwds: make(map[string]string),
	}
}

// streamEvent represents a single event from claude's stream-json output.
type streamEvent struct {
	Type      string         `json:"type"`
	SessionID string         `json:"session_id"`
	Result    string         `json:"result"`
	IsError   bool           `json:"is_error"`
	Message   *streamMessage `json:"message,omitempty"`
}

// streamMessage represents the message field in an assistant event.
type streamMessage struct {
	Content []streamContent `json:"content"`
}

// streamContent represents a content block in an assistant message.
type streamContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Info returns metadata about this agent.
func (a *CLIAgent) Info() AgentInfo {
	return AgentInfo{
		Name:    a.name,
		Type:    "cli",
		Model:   a.model,
		Command: a.command,
	}
}

// ResetSession clears the existing session for the given conversationID.
// Returns an empty string because the new session ID is only known after the
// next Chat call (claude assigns it during the conversation).
func (a *CLIAgent) ResetSession(_ context.Context, conversationID string) (string, error) {
	a.mu.Lock()
	delete(a.sessions, conversationID)
	a.mu.Unlock()
	log.Printf("[cli] session reset (command=%s, conversation=%s)", a.command, conversationID)
	return "", nil
}

// CurrentClaudeSession 返回当前微信会话绑定的 Claude Code session。
func (a *CLIAgent) CurrentClaudeSession(conversationID string) (string, bool) {
	if !a.isClaudeCLI() {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	sessionID := strings.TrimSpace(a.sessions[conversationID])
	return sessionID, sessionID != ""
}

// UseClaudeSession 绑定已有 Claude Code session，下一次 Chat 会通过 --resume 续接。
func (a *CLIAgent) UseClaudeSession(_ context.Context, conversationID string, sessionID string) error {
	if !a.isClaudeCLI() {
		return fmt.Errorf("agent is not claude cli")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("empty session id")
	}
	a.mu.Lock()
	a.sessions[conversationID] = sessionID
	a.mu.Unlock()
	return nil
}

// ClearClaudeSession 清理当前绑定，下一条 Claude 消息会创建新 session。
func (a *CLIAgent) ClearClaudeSession(conversationID string) {
	if !a.isClaudeCLI() {
		return
	}
	a.mu.Lock()
	delete(a.sessions, conversationID)
	a.mu.Unlock()
}

// SetCwd changes the working directory for subsequent CLI invocations.
func (a *CLIAgent) SetCwd(cwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cwd = cwd
}

// SetConversationCwd 固定单个 conversation 的工作目录，避免后台任务被全局 cwd 切换影响。
func (a *CLIAgent) SetConversationCwd(conversationID string, cwd string) {
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

func (a *CLIAgent) cwdForConversation(conversationID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cwd := strings.TrimSpace(a.conversationCwds[conversationID]); cwd != "" {
		return cwd
	}
	return a.cwd
}

// Chat sends a message to the CLI agent and returns the response.
func (a *CLIAgent) Chat(ctx context.Context, conversationID string, message string) (string, error) {
	switch a.name {
	case "codex":
		return a.chatCodex(ctx, conversationID, message)
	default:
		return a.chatClaude(ctx, conversationID, message)
	}
}

// isClaudeCLI 统一按 agent 名称和命令识别 Claude，支持用户给 Claude 配自定义别名。
func (a *CLIAgent) isClaudeCLI() bool {
	if strings.EqualFold(a.name, "claude") {
		return true
	}
	command := strings.ToLower(strings.TrimSpace(a.command))
	return strings.Contains(command, "claude")
}

// chatClaude uses claude -p with stream-json to get structured output and session persistence.
func (a *CLIAgent) chatClaude(ctx context.Context, conversationID string, message string) (string, error) {
	args := []string{"-p", message, "--output-format", "stream-json", "--verbose"}

	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	if a.systemPrompt != "" {
		args = append(args, "--append-system-prompt", a.systemPrompt)
	}
	// Append extra args from config (e.g. --dangerously-skip-permissions)
	args = append(args, a.args...)

	// Resume existing session for multi-turn conversation
	a.mu.Lock()
	sessionID, hasSession := a.sessions[conversationID]
	a.mu.Unlock()

	if hasSession {
		args = append(args, "--resume", sessionID)
		log.Printf("[cli] resuming session (command=%s, session=%s, conversation=%s)", a.command, sessionID, conversationID)
	} else {
		log.Printf("[cli] starting new conversation (command=%s, conversation=%s)", a.command, conversationID)
	}

	cmd := exec.CommandContext(ctx, a.command, args...)
	if cwd := a.cwdForConversation(conversationID); cwd != "" {
		cmd.Dir = cwd
	}
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			return "", fmt.Errorf("build %s env: %w", a.name, err)
		}
		cmd.Env = cmdEnv
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", a.name, err)
	}

	log.Printf("[cli] spawned process (command=%s, pid=%d, conversation=%s)", a.command, cmd.Process.Pid, conversationID)

	// Parse streaming JSON events
	var result string
	var newSessionID string
	var assistantTexts []string

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for large responses

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		// Capture session ID from any event
		if event.SessionID != "" {
			newSessionID = event.SessionID
		}

		switch event.Type {
		case "result":
			if event.IsError {
				return "", fmt.Errorf("%s returned error: %s", a.name, event.Result)
			}
			result = event.Result
		case "assistant":
			// Newer claude CLI versions send text in assistant events
			// instead of the result event's result field.
			if event.Message != nil {
				for _, c := range event.Message.Content {
					if c.Type == "text" && c.Text != "" {
						assistantTexts = append(assistantTexts, c.Text)
					}
				}
			}
		}
	}

	// If the result event had an empty result, fall back to accumulated assistant texts.
	if result == "" && len(assistantTexts) > 0 {
		result = strings.Join(assistantTexts, "")
	}

	if err := cmd.Wait(); err != nil {
		if result == "" {
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg != "" {
				return "", fmt.Errorf("%s exited with error: %w, stderr: %s", a.name, err, errMsg)
			}
			return "", fmt.Errorf("%s exited with error: %w", a.name, err)
		}
		// If we got a result but exit code is non-zero (e.g. hook failures), still return the result
	}

	log.Printf("[cli] process exited (command=%s, pid=%d)", a.command, cmd.Process.Pid)

	// Save session ID for multi-turn conversation
	if newSessionID != "" {
		a.mu.Lock()
		a.sessions[conversationID] = newSessionID
		a.mu.Unlock()
		log.Printf("[cli] saved session (session=%s, conversation=%s)", newSessionID, conversationID)
	}

	result = strings.TrimSpace(result)
	if result == "" {
		return "", fmt.Errorf("%s returned empty response", a.name)
	}

	return result, nil
}

// chatCodex handles codex CLI invocation using "codex exec".
func (a *CLIAgent) chatCodex(ctx context.Context, conversationID string, message string) (string, error) {
	args := []string{"exec", message}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	// Append extra args from config (e.g. --skip-git-repo-check)
	args = append(args, a.args...)

	log.Printf("[cli] running codex exec (command=%s)", a.command)
	cmd := exec.CommandContext(ctx, a.command, args...)
	if cwd := a.cwdForConversation(conversationID); cwd != "" {
		cmd.Dir = cwd
	}
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			return "", fmt.Errorf("build %s env: %w", a.name, err)
		}
		cmd.Env = cmdEnv
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("codex error: %w, stderr: %s", err, errMsg)
		}
		return "", fmt.Errorf("codex error: %w", err)
	}

	result := strings.TrimSpace(string(out))
	if result == "" {
		return "", fmt.Errorf("codex returned empty response")
	}
	return result, nil
}
