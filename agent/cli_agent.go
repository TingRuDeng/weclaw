package agent

import (
	"context"
	"fmt"
	"log"
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
	runAs            runAsUserSpec
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
	RunAsUser    string   // 以独立 Unix 用户运行（文件系统隔离）
	RunAsEnv     []string // run_as_user 时透传的环境变量名白名单
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
		runAs:            runAsUserSpec{User: cfg.RunAsUser, PreserveEnv: cfg.RunAsEnv},
		sessions:         make(map[string]string),
		conversationCwds: make(map[string]string),
	}
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
