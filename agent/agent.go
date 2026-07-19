package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
)

// ErrAgentSessionNotBound 表示当前消息窗口尚未绑定可继续的 Agent 会话。
var ErrAgentSessionNotBound = errors.New("Agent 会话未绑定")

// AgentInfo holds metadata about an agent for logging/debugging.
type AgentInfo struct {
	Name         string // e.g. "claude-acp", "claude", "gpt-4o"
	Type         string // e.g. "acp", "cli", "http"
	Model        string // e.g. "sonnet", "gpt-4o-mini"
	Effort       string // e.g. "medium", "high"
	Command      string // binary path, e.g. "/usr/local/bin/claude-agent-acp"
	LocalCommand string // 可选本地辅助命令；Claude 仅用于账号额度查询回退
	PID          int    // subprocess PID (0 if not applicable, e.g. http agent)
}

// String returns a human-readable summary for logging.
func (i AgentInfo) String() string {
	s := fmt.Sprintf("name=%s, type=%s, model=%s, command=%s", i.Name, i.Type, i.Model, i.Command)
	if i.Effort != "" {
		s += fmt.Sprintf(", effort=%s", i.Effort)
	}
	if i.PID > 0 {
		s += fmt.Sprintf(", pid=%d", i.PID)
	}
	return s
}

// defaultWorkspace returns ~/.weclaw/workspace as the default working directory.
func defaultWorkspace() string {
	home, err := config.DataDir()
	if err != nil {
		return os.TempDir()
	}
	dir := filepath.Join(home, "workspace")
	os.MkdirAll(dir, 0o755)
	return dir
}

// mergeEnv merges extra environment variables into the base environment.
func mergeEnv(base []string, extra map[string]string) ([]string, error) {
	if len(extra) == 0 {
		return base, nil
	}

	merged := append([]string(nil), base...)
	indexByKey := make(map[string]int, len(base))
	for i, entry := range merged {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		indexByKey[key] = i
	}

	newKeys := make([]string, 0, len(extra))
	for key, value := range extra {
		if key == "" || strings.Contains(key, "=") {
			return nil, fmt.Errorf("invalid env key %q", key)
		}
		entry := key + "=" + value
		if idx, ok := indexByKey[key]; ok {
			merged[idx] = entry
			continue
		}
		newKeys = append(newKeys, key)
	}

	sort.Strings(newKeys)
	for _, key := range newKeys {
		merged = append(merged, key+"="+extra[key])
	}

	return merged, nil
}

// Agent is the interface for AI chat agents.
type Agent interface {
	// Chat sends a message to the agent and returns the response.
	// conversationID is used to maintain conversation history per user.
	Chat(ctx context.Context, conversationID string, message string) (string, error)

	// ResetSession clears the existing session for the given conversationID and
	// starts a new one. Returns the new session ID if immediately available
	// (ACP mode), or an empty string if the ID will be assigned on next Chat
	// (CLI mode) or is not applicable (HTTP mode).
	ResetSession(ctx context.Context, conversationID string) (string, error)

	// Info returns metadata about this agent.
	Info() AgentInfo

	// SetCwd changes the working directory for subsequent operations.
	SetCwd(cwd string)
}

// CodexThreadAgent 暴露 Codex app-server 的 thread 控制能力。
type CodexThreadAgent interface {
	CurrentCodexThread(conversationID string) (string, bool)
	UseCodexThread(ctx context.Context, conversationID string, threadID string) error
	ClearCodexThread(conversationID string)
}

// CodexThreadState 描述 app-server 或 Desktop 持有的 Codex thread 当前运行态。
type CodexThreadState struct {
	ThreadID             string
	Model                string
	Effort               string
	Active               bool
	ActiveTurnID         string
	LastTurnID           string
	LastTurnStatus       string
	WaitingOnApproval    bool
	WaitingOnUserInput   bool
	Preview              string
	LastAgentMessageText string
}

// CodexThreadRuntimeAgent 暴露 Codex App 已运行 thread 的接管能力。
type CodexThreadRuntimeAgent interface {
	ReadCodexThreadState(ctx context.Context, conversationID string, threadID string) (CodexThreadState, error)
	WatchCodexThread(ctx context.Context, conversationID string, threadID string, onProgress func(delta string)) (string, error)
	SteerCodexThread(ctx context.Context, conversationID string, threadID string, turnID string, message string) error
	InterruptCodexThread(ctx context.Context, conversationID string, threadID string, turnID string) error
}

// CodexStructuredThreadRuntimeAgent 为已运行的 Codex turn 提供结构化观察事件。
type CodexStructuredThreadRuntimeAgent interface {
	WatchCodexThreadEvents(ctx context.Context, conversationID string, threadID string, onProgress func(ProgressEvent)) (string, error)
}

// ConversationWorkspaceAgent 允许 Agent 为单个 conversation 固定工作目录。
type ConversationWorkspaceAgent interface {
	SetConversationCwd(conversationID string, cwd string)
}

// ClaudeSession 描述 Claude ACP 返回的一个可恢复会话。
type ClaudeSession struct {
	ID        string
	Cwd       string
	Title     string
	UpdatedAt string
	Config    ClaudeSessionConfig
}

// ClaudeSessionConfig 描述 Claude session 当前模型与推理强度。
type ClaudeSessionConfig struct {
	Model  string
	Effort string
}

// ClaudeSessionCatalogAgent 暴露 Claude ACP 的会话目录能力。
type ClaudeSessionCatalogAgent interface {
	ListClaudeSessions(ctx context.Context) ([]ClaudeSession, error)
}

// ClaudeSessionAgent 暴露 Claude session 的绑定与清理能力。
type ClaudeSessionAgent interface {
	CurrentClaudeSession(conversationID string) (string, bool)
	UseClaudeSession(ctx context.Context, conversationID string, sessionID string) error
	ClearClaudeSession(conversationID string)
}

// ClaudeHostStatus describes the one process-resident ACP host shared by all
// WeClaw frontends. Bindings are frontend state; the host lifecycle is not.
type ClaudeHostStatus struct {
	Mode       string
	Started    bool
	PID        int
	Generation uint64
}

// ClaudeHostRuntimeAgent exposes the shared host lifecycle without granting a
// frontend permission to start an independent writer.
type ClaudeHostRuntimeAgent interface {
	ClaudeHostStatus() ClaudeHostStatus
}

// ClaudeSessionConfigUpdate 描述一次当前 Claude session 配置更新。
type ClaudeSessionConfigUpdate struct {
	ConversationID string
	Model          string
	Effort         string
}

// ClaudeSessionConfigAgent 暴露 Claude session 配置查询和更新能力。
type ClaudeSessionConfigAgent interface {
	ClaudeSessionConfig(conversationID string) (ClaudeSessionConfig, bool)
	SetClaudeSessionConfig(ctx context.Context, update ClaudeSessionConfigUpdate) error
}

// ClaudeModelStatus 表示当前 WeClaw 传给 Claude Code 的模型配置。
type ClaudeModelStatus struct {
	Model  string
	Effort string
}

// ClaudeModel 表示 Claude Code 可展示的一个模型选项。
type ClaudeModel struct {
	ID            string
	Name          string
	Alias         string
	Description   string
	EffortOptions []string
}

// ClaudeModelAgent 暴露 Claude Code 的模型配置查询能力。
type ClaudeModelAgent interface {
	ClaudeModelStatus() ClaudeModelStatus
	ListClaudeModels(ctx context.Context) ([]ClaudeModel, error)
}

// ClaudeModelControlAgent 暴露 Claude 运行时模型和推理强度切换能力。
type ClaudeModelControlAgent interface {
	ClaudeModelAgent
	SetClaudeModel(model string, effort string)
}

// ClaudeQuota 表示 Claude Code 登录账号的额度快照。
type ClaudeQuota struct {
	SubscriptionType    string
	RateLimitsAvailable bool
	Limits              []ClaudeRateLimit
	ExtraUsage          *ClaudeExtraUsage
}

// ClaudeRateLimit 表示一个 Claude 订阅额度窗口。
type ClaudeRateLimit struct {
	ID          string
	Name        string
	UsedPercent *float64
	ResetsAt    string
}

// ClaudeExtraUsage 表示 Claude 订阅的额外用量状态。
type ClaudeExtraUsage struct {
	Enabled      bool
	UsedPercent  *float64
	MonthlyLimit *float64
	UsedCredits  *float64
	Currency     string
}

// ClaudeQuotaAgent 暴露 Claude Code 登录账号的额度查询能力。
type ClaudeQuotaAgent interface {
	ReadClaudeQuota(ctx context.Context) (ClaudeQuota, error)
}

// CodexModelStatus 表示当前 WeClaw 传给 Codex 的模型配置。
type CodexModelStatus struct {
	Model  string
	Effort string
}

// CodexModel 表示 Codex app-server 暴露的一个可用模型。
type CodexModel struct {
	ID            string
	Name          string
	EffortOptions []string
}

// CodexModelAgent 暴露 Codex app-server 的模型查询能力。
type CodexModelAgent interface {
	CodexModelStatus() CodexModelStatus
	ListCodexModels(ctx context.Context) ([]CodexModel, error)
}

// CodexModelControlAgent 暴露 Codex 运行时模型/推理强度切换能力。
type CodexModelControlAgent interface {
	CodexModelAgent
	SetCodexModel(model string, effort string)
}

// CodexThreadConfig 表示单个 Codex thread 对后续 turn 生效的模型配置。
type CodexThreadConfig struct {
	Model  string
	Effort string
}

// CodexThreadConfigUpdate 只更新非空字段，避免模型和推理强度互相覆盖。
type CodexThreadConfigUpdate struct {
	ConversationID string
	ThreadID       string
	Model          string
	Effort         string
}

// CodexThreadConfigAgent 通过 app-server 管理单个 thread 的运行配置。
type CodexThreadConfigAgent interface {
	CodexThreadConfig(context.Context, string, string) (CodexThreadConfig, error)
	SetCodexThreadConfig(context.Context, CodexThreadConfigUpdate) error
}

// CodexQuota 表示 Codex app-server 返回的账号额度快照。
type CodexQuota struct {
	Limits []CodexRateLimit
}

// CodexRateLimit 表示一个按 limit_id 区分的 Codex 额度桶。
type CodexRateLimit struct {
	ID          string
	Name        string
	PlanType    string
	ReachedType string
	Primary     *CodexRateLimitWindow
	Secondary   *CodexRateLimitWindow
	Credits     *CodexCredits
}

// CodexRateLimitWindow 表示一个时间窗口内的额度使用比例。
type CodexRateLimitWindow struct {
	UsedPercent        int
	ResetsAt           *int64
	WindowDurationMins *int64
}

// CodexCredits 表示账号余额类额度状态。
type CodexCredits struct {
	Balance    string
	HasCredits bool
	Unlimited  bool
}

// CodexQuotaAgent 暴露 Codex app-server 的账号额度查询能力。
type CodexQuotaAgent interface {
	ReadCodexQuota(ctx context.Context) (CodexQuota, error)
}

// VisibleCompanionAgent 支持显式打开或断开本地可见 Companion。
type VisibleCompanionAgent interface {
	OpenVisibleCompanion(ctx context.Context) error
	DetachVisibleCompanion() bool
}
