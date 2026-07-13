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
	effort           string
	runAs            runAsUserSpec
	mu               sync.Mutex
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
	Effort       string
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
		effort:           cfg.Effort,
		runAs:            runAsUserSpec{User: cfg.RunAsUser, PreserveEnv: cfg.RunAsEnv},
		conversationCwds: make(map[string]string),
	}
}

// Info returns metadata about this agent.
func (a *CLIAgent) Info() AgentInfo {
	a.mu.Lock()
	model, effort := a.model, a.effort
	a.mu.Unlock()
	return AgentInfo{
		Name:    a.name,
		Type:    "cli",
		Model:   model,
		Effort:  effort,
		Command: a.command,
	}
}

// ResetSession 保留通用 Agent 接口；无会话能力的 CLI Agent 无需清理状态。
func (a *CLIAgent) ResetSession(_ context.Context, conversationID string) (string, error) {
	log.Printf("[cli] session reset (command=%s, conversation=%s)", a.command, conversationID)
	return "", nil
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
	if !strings.EqualFold(a.name, "codex") {
		return "", fmt.Errorf("CLI Agent %q 不受支持；Claude 必须使用 ACP", a.name)
	}
	return a.chatCodex(ctx, conversationID, message)
}
