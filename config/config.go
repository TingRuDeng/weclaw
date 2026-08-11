package config

import (
	"fmt"
	"log"
	"strings"
	"time"
)

const maxFeishuMessageAgeSeconds = int64(time.Duration(1<<63-1) / time.Second)

// Config holds the application configuration.
type Config struct {
	DefaultAgent          string                    `json:"default_agent"`
	APIAddr               string                    `json:"api_addr,omitempty"`
	APIToken              string                    `json:"api_token,omitempty"`
	UpdateSource          string                    `json:"update_source,omitempty"`
	SaveDir               string                    `json:"save_dir,omitempty"`
	AllowedWorkspaceRoots []string                  `json:"allowed_workspace_roots,omitempty"`
	LegacyAdminUsers      []string                  `json:"admin_users,omitempty"`           // 仅兼容保留旧配置；运行时忽略且不会自动迁移
	RateLimitPerMinute    int                       `json:"rate_limit_per_minute,omitempty"` // 每用户每分钟最多触发 agent 次数；0=不限流
	AuditLog              *bool                     `json:"audit_log,omitempty"`             // 是否记录审计日志；缺省=开启
	AuditLogPath          string                    `json:"audit_log_path,omitempty"`        // 审计日志路径；空=~/.weclaw/audit.log
	Progress              ProgressConfig            `json:"progress,omitempty"`
	Agents                map[string]AgentConfig    `json:"agents"`
	Platforms             map[string]PlatformConfig `json:"platforms,omitempty"`
}

// PlatformConfig 保存单个平台的启用状态、访问控制和展示覆盖配置。
type PlatformConfig struct {
	Enabled               *bool             `json:"enabled,omitempty"`
	AllowedUsers          []string          `json:"allowed_users,omitempty"`
	DefaultAgent          string            `json:"default_agent,omitempty"`
	Progress              *ProgressConfig   `json:"progress,omitempty"`
	MessageAggregationMs  *int              `json:"message_aggregation_ms,omitempty"`
	RequireMentionInGroup *bool             `json:"require_mention_in_group,omitempty"`
	Bots                  []FeishuBotConfig `json:"bots,omitempty"`
}

// FeishuBotConfig 描述单个飞书机器人入口，secret 只允许保存在凭证文件中。
type FeishuBotConfig struct {
	Name                  string          `json:"name"`
	DisplayName           string          `json:"display_name,omitempty"`
	Aliases               []string        `json:"aliases,omitempty"`
	AppID                 string          `json:"app_id"`
	AllowedUsers          []string        `json:"allowed_users,omitempty"`
	DefaultAgent          string          `json:"default_agent,omitempty"`
	Progress              *ProgressConfig `json:"progress,omitempty"`
	RequireMentionInGroup *bool           `json:"require_mention_in_group,omitempty"`
	MaxMessageAgeSeconds  *int64          `json:"max_message_age_seconds,omitempty"`
}

// EffectiveRequireMentionInGroup 返回飞书群聊 @ 触发规则，默认要求 @bot。
func (c PlatformConfig) EffectiveRequireMentionInGroup() bool {
	return boolValueDefault(c.RequireMentionInGroup, true)
}

// EffectiveRequireMentionInGroup 返回单个飞书机器人群聊 @ 触发规则，默认要求 @bot。
func (c FeishuBotConfig) EffectiveRequireMentionInGroup() bool {
	return boolValueDefault(c.RequireMentionInGroup, true)
}

// AgentConfig holds configuration for a single agent.
type AgentConfig struct {
	Type             string            `json:"type"`                        // "acp", "cli", "http", or "companion"
	Command          string            `json:"command,omitempty"`           // binary path (cli/acp type)
	LocalCommand     string            `json:"local_command,omitempty"`     // Claude 账号额度查询回退使用的原生命令
	Args             []string          `json:"args,omitempty"`              // extra args for command (e.g. ["acp"] for cursor)
	Aliases          []string          `json:"aliases,omitempty"`           // custom trigger commands (e.g. ["gpt", "4o"])
	Cwd              string            `json:"cwd,omitempty"`               // working directory (workspace)
	Env              map[string]string `json:"env,omitempty"`               // extra environment variables (cli/acp type)
	Model            string            `json:"model,omitempty"`             // model name
	Effort           string            `json:"effort,omitempty"`            // Codex / Claude reasoning effort
	PermissionLevel  string            `json:"permission_level,omitempty"`  // Codex 权限档位：default / auto_review / full_access
	ApprovalPolicy   string            `json:"approval_policy,omitempty"`   // Codex approvalPolicy 高级覆盖
	ApprovalReviewer string            `json:"approval_reviewer,omitempty"` // Codex approvalsReviewer 高级覆盖：user / auto_review
	SandboxMode      string            `json:"sandbox_mode,omitempty"`      // Codex sandbox：read-only / workspace-write / danger-full-access
	SystemPrompt     string            `json:"system_prompt,omitempty"`     // system prompt
	Endpoint         string            `json:"endpoint,omitempty"`          // API endpoint (http type)
	APIKey           string            `json:"api_key,omitempty"`           // HTTP Agent 密钥；只能写入配置文件和 Authorization header，禁止日志/审计明文输出
	Headers          map[string]string `json:"headers,omitempty"`           // extra HTTP headers (http type)
	MaxHistory       int               `json:"max_history,omitempty"`       // max history (http type)
	Progress         *ProgressConfig   `json:"progress,omitempty"`          // 微信进度反馈配置
	AutoLaunch       *bool             `json:"auto_launch,omitempty"`       // companion 是否自动打开本地可见终端
	AppServerSocket  string            `json:"app_server_socket,omitempty"` // Codex 单一 app-server 的共享 Unix socket
	CodexHostMode    string            `json:"codex_host_mode,omitempty"`   // Codex Host：auto / daemon / managed
	CodexAutoUpdate  string            `json:"codex_auto_update,omitempty"` // Codex CLI 自动更新：off / incompatible
	RunAsUser        string            `json:"run_as_user,omitempty"`       // 以独立 Unix 用户运行 agent，做文件系统隔离
	RunAsEnv         []string          `json:"run_as_env,omitempty"`        // run_as_user 时需透传的环境变量名白名单
}

// EffectiveApprovalPolicy 返回 Codex ACP 会话使用的审批策略；未配置档位时使用 default。
func (c AgentConfig) EffectiveApprovalPolicy() string {
	if policy := strings.TrimSpace(c.ApprovalPolicy); policy != "" {
		return policy
	}
	switch normalizePermissionLevel(c.PermissionLevel) {
	case "", "default", "auto_review":
		return "on-request"
	case "full_access":
		return "never"
	default:
		return ""
	}
}

// EffectiveSandboxMode 返回 Codex ACP 会话使用的沙箱模式；未配置档位时使用 default。
func (c AgentConfig) EffectiveSandboxMode() string {
	if mode := strings.TrimSpace(c.SandboxMode); mode != "" {
		return mode
	}
	switch normalizePermissionLevel(c.PermissionLevel) {
	case "", "default", "auto_review":
		return "workspace-write"
	case "full_access":
		return "danger-full-access"
	default:
		return ""
	}
}

// EffectiveApprovalReviewer 返回 Codex app-server 的审批 reviewer；未配置档位时使用 default。
func (c AgentConfig) EffectiveApprovalReviewer() string {
	if reviewer := strings.TrimSpace(c.ApprovalReviewer); reviewer != "" {
		return reviewer
	}
	switch normalizePermissionLevel(c.PermissionLevel) {
	case "", "default":
		return "user"
	case "auto_review":
		return "auto_review"
	default:
		return ""
	}
}

// ValidateCodexPermissionConfig 在启动前拒绝旧权限档位，避免静默落到错误审批体验。
func (c AgentConfig) ValidateCodexPermissionConfig() error {
	level := normalizePermissionLevel(c.PermissionLevel)
	switch level {
	case "", "default", "auto_review", "full_access":
	default:
		return fmt.Errorf("invalid permission_level %q: use default, auto_review, or full_access", c.PermissionLevel)
	}
	reviewer := strings.TrimSpace(c.ApprovalReviewer)
	switch reviewer {
	case "", "user", "auto_review":
		return nil
	default:
		return fmt.Errorf("invalid approval_reviewer %q: use user or auto_review", c.ApprovalReviewer)
	}
}

// EffectiveCodexAutoUpdate 返回 Codex CLI 自动更新策略。未显式迁移的自定义
// Agent 保持关闭；NormalizeCodexRemoteFirst 会为原生 shared app-server 写入推荐值。
func (c AgentConfig) EffectiveCodexAutoUpdate() string {
	return normalizeCodexAutoUpdate(c.CodexAutoUpdate)
}

// ValidateCodexAutoUpdateConfig 拒绝未知策略，避免把拼写错误静默当成关闭。
func (c AgentConfig) ValidateCodexAutoUpdateConfig() error {
	switch policy := c.EffectiveCodexAutoUpdate(); policy {
	case "", "off", "incompatible":
		return nil
	default:
		return fmt.Errorf("invalid codex_auto_update %q: use off or incompatible", c.CodexAutoUpdate)
	}
}

// EffectiveCodexHostMode 返回 Codex Host 生命周期策略。auto 会在运行时仅当
// 官方 standalone daemon 可用时启用 daemon；否则保留兼容的 WeClaw managed
// Host。显式 daemon 从不静默回退到第二个 Host。
func (c AgentConfig) EffectiveCodexHostMode() string {
	mode := normalizeCodexHostMode(c.CodexHostMode)
	if mode == "" {
		return "auto"
	}
	return mode
}

// ValidateCodexHostModeConfig 拒绝 daemon 与自定义 socket/run_as_user 混用；
// 官方 daemon 的 socket 与 Unix 用户命名空间由 CODEX_HOME 决定。
func (c AgentConfig) ValidateCodexHostModeConfig() error {
	switch mode := c.EffectiveCodexHostMode(); mode {
	case "auto", "managed":
		return nil
	case "daemon":
		if strings.TrimSpace(c.AppServerSocket) != "" {
			return fmt.Errorf("codex_host_mode daemon cannot be combined with app_server_socket")
		}
		if strings.TrimSpace(c.RunAsUser) != "" {
			return fmt.Errorf("codex_host_mode daemon cannot be combined with run_as_user")
		}
		return nil
	default:
		return fmt.Errorf("invalid codex_host_mode %q: use auto, daemon, or managed", c.CodexHostMode)
	}
}

func normalizeCodexHostMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	return strings.ReplaceAll(mode, "-", "_")
}

func normalizeCodexAutoUpdate(policy string) string {
	policy = strings.ToLower(strings.TrimSpace(policy))
	return strings.ReplaceAll(policy, "-", "_")
}

func normalizePermissionLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	return strings.ReplaceAll(level, "-", "_")
}

// BuildAliasMap builds a map from custom alias to agent name from all agent configs.
// It logs warnings for conflicts: duplicate aliases and aliases shadowing agent keys.
func BuildAliasMap(agents map[string]AgentConfig) map[string]string {
	// Built-in commands that cannot be overridden
	reserved := map[string]bool{
		"info": true, "help": true, "new": true, "clear": true, "cwd": true,
	}

	m := make(map[string]string)
	for name, cfg := range agents {
		for _, alias := range cfg.Aliases {
			if reserved[alias] {
				log.Printf("[config] WARNING: alias %q for agent %q conflicts with built-in command, ignored", alias, name)
				continue
			}
			if existing, ok := m[alias]; ok {
				log.Printf("[config] WARNING: alias %q is defined by both %q and %q, using %q", alias, existing, name, name)
			}
			m[alias] = name
		}
	}

	// Warn if a custom alias shadows an agent key
	for alias, target := range m {
		if _, isAgent := agents[alias]; isAgent && alias != target {
			log.Printf("[config] WARNING: alias %q (-> %q) shadows agent key %q", alias, target, alias)
		}
	}

	return m
}

// DefaultConfig returns an empty configuration.
func DefaultConfig() *Config {
	return &Config{
		UpdateSource: "auto",
		Progress:     DefaultProgressConfig(),
		Agents:       make(map[string]AgentConfig),
		Platforms:    make(map[string]PlatformConfig),
	}
}

// boolValueDefault 读取可选布尔值，缺省时返回业务安全默认值。
func boolValueDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

// Validate 检查配置中会影响运行时安全边界的字段。
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(c.UpdateSource)) {
	case "", "auto", "github", "gitee":
	default:
		return fmt.Errorf("update_source must be auto, github, or gitee; got %q", c.UpdateSource)
	}
	if err := validateProgressConfig("progress", &c.Progress); err != nil {
		return err
	}
	for name, agentCfg := range c.Agents {
		if err := validateProgressConfig(fmt.Sprintf("agent %q progress", name), agentCfg.Progress); err != nil {
			return err
		}
		if agentCfg.MaxHistory < 0 {
			return fmt.Errorf("agent %q: max_history must be >= 0", name)
		}
		if err := agentCfg.ValidateCodexPermissionConfig(); err != nil {
			return fmt.Errorf("agent %q: %w", name, err)
		}
		if err := agentCfg.ValidateCodexAutoUpdateConfig(); err != nil {
			return fmt.Errorf("agent %q: %w", name, err)
		}
		if err := agentCfg.ValidateCodexHostModeConfig(); err != nil {
			return fmt.Errorf("agent %q: %w", name, err)
		}
	}
	for name, platformCfg := range c.Platforms {
		if err := validateProgressConfig(fmt.Sprintf("platform %q progress", name), platformCfg.Progress); err != nil {
			return err
		}
		for _, bot := range platformCfg.Bots {
			if err := validateProgressConfig(fmt.Sprintf("platform %q bot %q progress", name, bot.Name), bot.Progress); err != nil {
				return err
			}
		}
	}
	if err := validateFeishuPlatformConfig(c.Platforms["feishu"]); err != nil {
		return err
	}
	return nil
}

func validateProgressConfig(scope string, cfg *ProgressConfig) error {
	if cfg != nil && cfg.StreamTimelineLimit != nil && *cfg.StreamTimelineLimit < 0 {
		return fmt.Errorf("%s: stream_timeline_limit must be >= 0", scope)
	}
	return nil
}

func validateFeishuPlatformConfig(platformCfg PlatformConfig) error {
	if hasLegacyFeishuConfig(platformCfg) {
		return fmt.Errorf("platforms.feishu legacy single-bot fields are not supported; use platforms.feishu.bots")
	}
	if platformCfg.Enabled != nil && *platformCfg.Enabled && len(platformCfg.Bots) == 0 {
		return fmt.Errorf("platforms.feishu.bots is required when feishu is enabled")
	}
	seenNames := make(map[string]struct{}, len(platformCfg.Bots))
	seenAppIDs := make(map[string]struct{}, len(platformCfg.Bots))
	for _, bot := range platformCfg.Bots {
		name := strings.TrimSpace(bot.Name)
		appID := strings.TrimSpace(bot.AppID)
		if name == "" {
			return fmt.Errorf("platforms.feishu.bots contains empty bot name")
		}
		if appID == "" {
			return fmt.Errorf("platforms.feishu.bots[%q] app_id is required", name)
		}
		if bot.MaxMessageAgeSeconds != nil && *bot.MaxMessageAgeSeconds < 0 {
			return fmt.Errorf("platforms.feishu.bots[%q] max_message_age_seconds must be >= 0", name)
		}
		if bot.MaxMessageAgeSeconds != nil && *bot.MaxMessageAgeSeconds > maxFeishuMessageAgeSeconds {
			return fmt.Errorf("platforms.feishu.bots[%q] max_message_age_seconds must be <= %d", name, maxFeishuMessageAgeSeconds)
		}
		if _, ok := seenNames[name]; ok {
			return fmt.Errorf("duplicate feishu bot name %q", name)
		}
		if _, ok := seenAppIDs[appID]; ok {
			return fmt.Errorf("duplicate feishu bot app_id %q", appID)
		}
		seenNames[name] = struct{}{}
		seenAppIDs[appID] = struct{}{}
	}
	if err := validateFeishuBotReferences(platformCfg.Bots); err != nil {
		return err
	}
	return nil
}

func hasLegacyFeishuConfig(platformCfg PlatformConfig) bool {
	return len(platformCfg.AllowedUsers) > 0 ||
		strings.TrimSpace(platformCfg.DefaultAgent) != "" ||
		platformCfg.Progress != nil ||
		platformCfg.RequireMentionInGroup != nil
}
