package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds the application configuration.
type Config struct {
	DefaultAgent string                    `json:"default_agent"`
	APIAddr      string                    `json:"api_addr,omitempty"`
	APIToken     string                    `json:"api_token,omitempty"`
	SaveDir      string                    `json:"save_dir,omitempty"`
	AllowedWorkspaceRoots []string         `json:"allowed_workspace_roots,omitempty"`
	Progress     ProgressConfig            `json:"progress,omitempty"`
	Agents       map[string]AgentConfig    `json:"agents"`
	Platforms    map[string]PlatformConfig `json:"platforms,omitempty"`
}

// PlatformConfig 保存单个平台的启用状态、访问控制和展示覆盖配置。
type PlatformConfig struct {
	Enabled               *bool           `json:"enabled,omitempty"`
	AllowedUsers          []string        `json:"allowed_users,omitempty"`
	DefaultAgent          string          `json:"default_agent,omitempty"`
	Progress              *ProgressConfig `json:"progress,omitempty"`
	MessageAggregationMs  *int            `json:"message_aggregation_ms,omitempty"`
	RequireMentionInGroup *bool           `json:"require_mention_in_group,omitempty"`
	ThreadIsolation       *bool           `json:"thread_isolation,omitempty"`
}

// EffectiveRequireMentionInGroup 返回飞书群聊 @ 触发规则，默认要求 @bot。
func (c PlatformConfig) EffectiveRequireMentionInGroup() bool {
	return boolValueDefault(c.RequireMentionInGroup, true)
}

// EffectiveThreadIsolation 返回飞书 thread 会话隔离规则，默认按 thread 隔离。
func (c PlatformConfig) EffectiveThreadIsolation() bool {
	return boolValueDefault(c.ThreadIsolation, true)
}

// AgentConfig holds configuration for a single agent.
type AgentConfig struct {
	Type         string            `json:"type"`                    // "acp", "cli", "http", or "companion"
	Command      string            `json:"command,omitempty"`       // binary path (cli/acp type)
	Args         []string          `json:"args,omitempty"`          // extra args for command (e.g. ["acp"] for cursor)
	Aliases      []string          `json:"aliases,omitempty"`       // custom trigger commands (e.g. ["gpt", "4o"])
	Cwd          string            `json:"cwd,omitempty"`           // working directory (workspace)
	Env          map[string]string `json:"env,omitempty"`           // extra environment variables (cli/acp type)
	Model        string            `json:"model,omitempty"`         // model name
	Effort       string            `json:"effort,omitempty"`        // Codex reasoning effort
	SystemPrompt string            `json:"system_prompt,omitempty"` // system prompt
	Endpoint     string            `json:"endpoint,omitempty"`      // API endpoint (http type)
	APIKey       string            `json:"api_key,omitempty"`       // API key (http type)
	Headers      map[string]string `json:"headers,omitempty"`       // extra HTTP headers (http type)
	MaxHistory   int               `json:"max_history,omitempty"`   // max history (http type)
	Progress     *ProgressConfig   `json:"progress,omitempty"`      // 微信进度反馈配置
	AutoLaunch   *bool             `json:"auto_launch,omitempty"`   // companion 是否自动打开本地可见终端
	RunAsUser    string            `json:"run_as_user,omitempty"`   // 以独立 Unix 用户运行 agent，做文件系统隔离
	RunAsEnv     []string          `json:"run_as_env,omitempty"`    // run_as_user 时需透传的环境变量名白名单
}

// ProgressConfig 控制微信侧进度反馈的展示粒度。
type ProgressConfig struct {
	Mode                   string `json:"mode,omitempty"`
	SendAcceptance         *bool  `json:"send_acceptance,omitempty"`
	EnableTyping           *bool  `json:"enable_typing,omitempty"`
	TypingHeartbeatSeconds int    `json:"typing_heartbeat_seconds,omitempty"`
	InitialDelaySeconds    int    `json:"initial_delay_seconds,omitempty"`
	SummaryIntervalSeconds int    `json:"summary_interval_seconds,omitempty"`
	MaxProgressMessages    int    `json:"max_progress_messages,omitempty"`
	ShowTextPreview        *bool  `json:"show_text_preview,omitempty"`
	PreviewRunes           int    `json:"preview_runes,omitempty"`
	MaxTailRunes           int    `json:"max_tail_runes,omitempty"`
	DuplicateTTLSeconds    int    `json:"duplicate_ttl_seconds,omitempty"`
	TaskTimeoutSeconds     int    `json:"task_timeout_seconds,omitempty"`
	IncludePartialOnError  *bool  `json:"include_partial_on_error,omitempty"`
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
		Progress:  DefaultProgressConfig(),
		Agents:    make(map[string]AgentConfig),
		Platforms: make(map[string]PlatformConfig),
	}
}

// DefaultProgressConfig 返回微信场景下更安静的默认进度体验。
func DefaultProgressConfig() ProgressConfig {
	sendAcceptance := false
	enableTyping := true
	showTextPreview := false
	includePartialOnError := false

	return ProgressConfig{
		Mode:                   "typing",
		SendAcceptance:         &sendAcceptance,
		EnableTyping:           &enableTyping,
		TypingHeartbeatSeconds: 8,
		InitialDelaySeconds:    10,
		SummaryIntervalSeconds: 20,
		MaxProgressMessages:    4,
		ShowTextPreview:        &showTextPreview,
		PreviewRunes:           180,
		MaxTailRunes:           1800,
		DuplicateTTLSeconds:    300,
		TaskTimeoutSeconds:     0,
		IncludePartialOnError:  &includePartialOnError,
	}
}

// boolValueDefault 读取可选布尔值，缺省时返回业务安全默认值。
func boolValueDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

// NormalizeProgressConfig 用局部配置覆盖基础配置，未填写字段沿用基础值。
func NormalizeProgressConfig(base ProgressConfig, override *ProgressConfig) ProgressConfig {
	if override == nil {
		return base
	}

	cfg := base
	if override.Mode != "" {
		cfg.Mode = override.Mode
	}
	if override.SendAcceptance != nil {
		cfg.SendAcceptance = override.SendAcceptance
	}
	if override.EnableTyping != nil {
		cfg.EnableTyping = override.EnableTyping
	}
	cfg = mergeProgressNumbers(cfg, *override)
	if override.ShowTextPreview != nil {
		cfg.ShowTextPreview = override.ShowTextPreview
	}
	if override.IncludePartialOnError != nil {
		cfg.IncludePartialOnError = override.IncludePartialOnError
	}
	return cfg
}

func mergeProgressNumbers(base ProgressConfig, override ProgressConfig) ProgressConfig {
	if override.TypingHeartbeatSeconds > 0 {
		base.TypingHeartbeatSeconds = override.TypingHeartbeatSeconds
	}
	if override.InitialDelaySeconds > 0 {
		base.InitialDelaySeconds = override.InitialDelaySeconds
	}
	if override.SummaryIntervalSeconds > 0 {
		base.SummaryIntervalSeconds = override.SummaryIntervalSeconds
	}
	if override.MaxProgressMessages > 0 {
		base.MaxProgressMessages = override.MaxProgressMessages
	}
	if override.PreviewRunes > 0 {
		base.PreviewRunes = override.PreviewRunes
	}
	if override.MaxTailRunes > 0 {
		base.MaxTailRunes = override.MaxTailRunes
	}
	if override.DuplicateTTLSeconds > 0 {
		base.DuplicateTTLSeconds = override.DuplicateTTLSeconds
	}
	if override.TaskTimeoutSeconds > 0 {
		base.TaskTimeoutSeconds = override.TaskTimeoutSeconds
	}
	return base
}

// ConfigPath returns the path to the config file.
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "config.json"), nil
}

// Load loads configuration from disk and environment variables.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err != nil {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			loadEnv(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentConfig)
	}
	if cfg.Platforms == nil {
		cfg.Platforms = make(map[string]PlatformConfig)
	}
	cfg.Progress = NormalizeProgressConfig(DefaultProgressConfig(), &cfg.Progress)

	loadEnv(cfg)
	return cfg, nil
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("WECLAW_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgent = v
	}
	if v := os.Getenv("WECLAW_API_ADDR"); v != "" {
		cfg.APIAddr = v
	}
	if v := os.Getenv("WECLAW_API_TOKEN"); v != "" {
		cfg.APIToken = v
	}
	if v := os.Getenv("WECLAW_SAVE_DIR"); v != "" {
		cfg.SaveDir = v
	}
	loadProgressEnv(cfg)
}

func loadProgressEnv(cfg *Config) {
	if v := os.Getenv("WECLAW_PROGRESS_MODE"); v != "" {
		cfg.Progress.Mode = v
	}
	setProgressIntEnv("WECLAW_PROGRESS_SUMMARY_INTERVAL_SECONDS", &cfg.Progress.SummaryIntervalSeconds)
	setProgressIntEnv("WECLAW_PROGRESS_MAX_MESSAGES", &cfg.Progress.MaxProgressMessages)
}

func setProgressIntEnv(name string, target *int) {
	v := os.Getenv(name)
	if v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("[config] WARNING: invalid %s=%q: %v", name, v, err)
		return
	}
	*target = n
}

// Save saves the configuration to disk.
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0o600)
}
