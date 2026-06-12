package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentInfo holds metadata about an agent for logging/debugging.
type AgentInfo struct {
	Name    string // e.g. "claude-acp", "claude", "gpt-4o"
	Type    string // e.g. "acp", "cli", "http"
	Model   string // e.g. "sonnet", "gpt-4o-mini"
	Effort  string // e.g. "medium", "high"
	Command string // binary path, e.g. "/usr/local/bin/claude-agent-acp"
	PID     int    // subprocess PID (0 if not applicable, e.g. http agent)
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
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	dir := filepath.Join(home, ".weclaw", "workspace")
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

// ClaudeSessionAgent 暴露 Claude Code CLI 的 session 控制能力。
type ClaudeSessionAgent interface {
	CurrentClaudeSession(conversationID string) (string, bool)
	UseClaudeSession(ctx context.Context, conversationID string, sessionID string) error
	ClearClaudeSession(conversationID string)
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
