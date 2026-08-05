package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

// ConfigPath 返回配置文件路径。
func ConfigPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// DataDir 返回 WeClaw 自有状态根目录，显式 WECLAW_HOME 优先。
func DataDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("WECLAW_HOME")); override != "" {
		dir, err := filepath.Abs(filepath.Clean(override))
		if err != nil {
			return "", fmt.Errorf("resolve WECLAW_HOME: %w", err)
		}
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw"), nil
}

// Load 从磁盘和环境变量加载配置。
func Load() (*Config, error) {
	cfg := DefaultConfig()
	path, err := ConfigPath()
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	if err := securefile.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("secure config directory: %w", err)
	}
	data, err := securefile.Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return finalizeLoadedConfig(cfg)
		}
		if strings.Contains(err.Error(), "permissions must be 0600") {
			return nil, fmt.Errorf("read config: %w; fix with chmod 600 %q", err, path)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return finalizeLoadedConfig(cfg)
}

// finalizeLoadedConfig 统一默认配置和文件配置的标准化、环境覆盖与校验顺序。
func finalizeLoadedConfig(cfg *Config) (*Config, error) {
	normalizeLoadedConfig(cfg)
	loadEnv(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func normalizeLoadedConfig(cfg *Config) {
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentConfig)
	}
	if cfg.Platforms == nil {
		cfg.Platforms = make(map[string]PlatformConfig)
	}
	if strings.TrimSpace(cfg.UpdateSource) == "" {
		cfg.UpdateSource = "auto"
	} else {
		cfg.UpdateSource = strings.ToLower(strings.TrimSpace(cfg.UpdateSource))
	}
	cfg.Progress = NormalizeProgressConfig(DefaultProgressConfig(), &cfg.Progress)
}

func loadEnv(cfg *Config) {
	envStrings := []struct {
		name   string
		target *string
	}{
		{"WECLAW_DEFAULT_AGENT", &cfg.DefaultAgent}, {"WECLAW_API_ADDR", &cfg.APIAddr},
		{"WECLAW_API_TOKEN", &cfg.APIToken}, {"WECLAW_SAVE_DIR", &cfg.SaveDir},
		{"WECLAW_UPDATE_SOURCE", &cfg.UpdateSource},
	}
	for _, item := range envStrings {
		if value := os.Getenv(item.name); value != "" {
			*item.target = value
		}
	}
	loadProgressEnv(cfg)
}

func loadProgressEnv(cfg *Config) {
	if value := os.Getenv("WECLAW_PROGRESS_MODE"); value != "" {
		cfg.Progress.Mode = value
	}
	setProgressIntEnv("WECLAW_PROGRESS_SUMMARY_INTERVAL_SECONDS", &cfg.Progress.SummaryIntervalSeconds)
	setProgressIntEnv("WECLAW_PROGRESS_MAX_MESSAGES", &cfg.Progress.MaxProgressMessages)
}

func setProgressIntEnv(name string, target *int) {
	value := os.Getenv(name)
	if value == "" {
		return
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("[config] WARNING: invalid %s=%q: %v", name, value, err)
		return
	}
	*target = number
}

// Save 原子保存配置，避免异常退出留下截断文件。
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := securefile.Write(path, data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
