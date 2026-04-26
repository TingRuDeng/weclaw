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
	DefaultAgent string                 `json:"default_agent"`
	APIAddr      string                 `json:"api_addr,omitempty"`
	SaveDir      string                 `json:"save_dir,omitempty"`
	Progress     ProgressConfig         `json:"progress,omitempty"`
	Agents       map[string]AgentConfig `json:"agents"`
}

// AgentConfig holds configuration for a single agent.
type AgentConfig struct {
	Type         string            `json:"type"`                    // "acp", "cli", or "http"
	Command      string            `json:"command,omitempty"`       // binary path (cli/acp type)
	Args         []string          `json:"args,omitempty"`          // extra args for command (e.g. ["acp"] for cursor)
	Aliases      []string          `json:"aliases,omitempty"`       // custom trigger commands (e.g. ["gpt", "4o"])
	Cwd          string            `json:"cwd,omitempty"`           // working directory (workspace)
	Env          map[string]string `json:"env,omitempty"`           // extra environment variables (cli/acp type)
	Model        string            `json:"model,omitempty"`         // model name
	SystemPrompt string            `json:"system_prompt,omitempty"` // system prompt
	Endpoint     string            `json:"endpoint,omitempty"`      // API endpoint (http type)
	APIKey       string            `json:"api_key,omitempty"`       // API key (http type)
	Headers      map[string]string `json:"headers,omitempty"`       // extra HTTP headers (http type)
	MaxHistory   int               `json:"max_history,omitempty"`   // max history (http type)
	Progress     *ProgressConfig   `json:"progress,omitempty"`      // 微信进度反馈配置
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
		Progress: DefaultProgressConfig(),
		Agents:   make(map[string]AgentConfig),
	}
}

// DefaultProgressConfig 返回微信场景下更可读的默认进度体验。
func DefaultProgressConfig() ProgressConfig {
	sendAcceptance := true
	enableTyping := true
	showTextPreview := false
	includePartialOnError := false

	return ProgressConfig{
		Mode:                   "summary",
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
