package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		return filepath.Clean(override), nil
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
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return finalizeLoadedConfig(cfg)
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
	cfg.Progress = NormalizeProgressConfig(DefaultProgressConfig(), &cfg.Progress)
}

func loadEnv(cfg *Config) {
	envStrings := []struct {
		name   string
		target *string
	}{
		{"WECLAW_DEFAULT_AGENT", &cfg.DefaultAgent}, {"WECLAW_API_ADDR", &cfg.APIAddr},
		{"WECLAW_API_TOKEN", &cfg.APIToken}, {"WECLAW_SAVE_DIR", &cfg.SaveDir},
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return replaceConfig(path, data)
}

func replaceConfig(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create config temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := writeConfigTemp(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func writeConfigTemp(tmp *os.File, data []byte) error {
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod config temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync config temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close config temp file: %w", err)
	}
	return nil
}
