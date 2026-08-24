package config

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

var configMutationMu sync.Mutex

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
	path, err := ConfigPath()
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	cfg, err := loadPersistedConfigAtPath(path)
	if err != nil {
		return nil, err
	}
	loadEnv(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadPersistedConfigAtPath 只读取磁盘配置，不应用任何环境变量覆盖。
func loadPersistedConfigAtPath(path string) (*Config, error) {
	cfg := DefaultConfig()
	if err := securefile.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("secure config directory: %w", err)
	}
	data, err := securefile.Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			normalizeLoadedConfig(cfg)
			return cfg, nil
		}
		if strings.Contains(err.Error(), "permissions must be 0600") {
			return nil, fmt.Errorf("read config: %w; fix with chmod 600 %q", err, path)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	normalizeLoadedConfig(cfg)
	return cfg, nil
}

// EnsureAPIToken 在配置锁内一次性生成并持久化 API Token。
func EnsureAPIToken() (string, error) {
	return ensureAPITokenWithReader(rand.Reader)
}

func ensureAPITokenWithReader(random io.Reader) (string, error) {
	if random == nil {
		return "", fmt.Errorf("API token random source is nil")
	}
	path, err := ConfigPath()
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	var token string
	err = withConfigMutationLock(path, func() error {
		cfg, err := loadPersistedConfigAtPath(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(cfg.APIToken) != "" {
			token = cfg.APIToken
			return nil
		}
		bytes := make([]byte, 32)
		if _, err := io.ReadFull(random, bytes); err != nil {
			return fmt.Errorf("generate API token: %w", err)
		}
		token = base64.RawURLEncoding.EncodeToString(bytes)
		cfg.APIToken = token
		if err := cfg.Validate(); err != nil {
			return err
		}
		return saveConfigAtPath(path, cfg)
	})
	if err != nil {
		return "", err
	}
	return token, nil
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
	setProgressIntPointerEnv("WECLAW_PROGRESS_STREAM_TIMELINE_LIMIT", &cfg.Progress.StreamTimelineLimit)
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

func setProgressIntPointerEnv(name string, target **int) {
	value := os.Getenv(name)
	if value == "" {
		return
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("[config] WARNING: invalid %s=%q: %v", name, value, err)
		return
	}
	*target = &number
}

// Save 原子保存配置，避免异常退出留下截断文件。
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	return withConfigMutationLock(path, func() error {
		return saveConfigAtPath(path, cfg)
	})
}

// Update 在进程内和跨进程锁内重新读取最新配置、执行一次修改并原子保存。
// 所有基于旧配置的读改写都应走该入口，避免并发命令相互覆盖。
func Update(mutate func(*Config) error) error {
	if mutate == nil {
		return fmt.Errorf("config update mutation is nil")
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	return withConfigMutationLock(path, func() error {
		cfg, err := loadPersistedConfigAtPath(path)
		if err != nil {
			return err
		}
		persistedEnvValues := captureEnvironmentBackedValues(cfg)
		loadEnv(cfg)
		effectiveEnvValues := captureEnvironmentBackedValues(cfg)
		if err := mutate(cfg); err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		restoreUnchangedEnvironmentBackedValues(cfg, persistedEnvValues, effectiveEnvValues)
		if err := cfg.Validate(); err != nil {
			return err
		}
		if err := saveConfigAtPath(path, cfg); err != nil {
			return err
		}
		loadEnv(cfg)
		return nil
	})
}

type environmentBackedValues struct {
	defaultAgent                string
	apiAddr                     string
	apiToken                    string
	saveDir                     string
	updateSource                string
	progressMode                string
	progressSummaryInterval     int
	progressMaxMessages         int
	progressStreamTimelineLimit *int
}

func captureEnvironmentBackedValues(cfg *Config) environmentBackedValues {
	return environmentBackedValues{
		defaultAgent:                cfg.DefaultAgent,
		apiAddr:                     cfg.APIAddr,
		apiToken:                    cfg.APIToken,
		saveDir:                     cfg.SaveDir,
		updateSource:                cfg.UpdateSource,
		progressMode:                cfg.Progress.Mode,
		progressSummaryInterval:     cfg.Progress.SummaryIntervalSeconds,
		progressMaxMessages:         cfg.Progress.MaxProgressMessages,
		progressStreamTimelineLimit: cfg.Progress.StreamTimelineLimit,
	}
}

func restoreUnchangedEnvironmentBackedValues(cfg *Config, persisted, effective environmentBackedValues) {
	if os.Getenv("WECLAW_DEFAULT_AGENT") != "" && cfg.DefaultAgent == effective.defaultAgent {
		cfg.DefaultAgent = persisted.defaultAgent
	}
	if os.Getenv("WECLAW_API_ADDR") != "" && cfg.APIAddr == effective.apiAddr {
		cfg.APIAddr = persisted.apiAddr
	}
	if os.Getenv("WECLAW_API_TOKEN") != "" && cfg.APIToken == effective.apiToken {
		cfg.APIToken = persisted.apiToken
	}
	if os.Getenv("WECLAW_SAVE_DIR") != "" && cfg.SaveDir == effective.saveDir {
		cfg.SaveDir = persisted.saveDir
	}
	if os.Getenv("WECLAW_UPDATE_SOURCE") != "" && cfg.UpdateSource == effective.updateSource {
		cfg.UpdateSource = persisted.updateSource
	}
	if os.Getenv("WECLAW_PROGRESS_MODE") != "" && cfg.Progress.Mode == effective.progressMode {
		cfg.Progress.Mode = persisted.progressMode
	}
	if os.Getenv("WECLAW_PROGRESS_SUMMARY_INTERVAL_SECONDS") != "" && cfg.Progress.SummaryIntervalSeconds == effective.progressSummaryInterval {
		cfg.Progress.SummaryIntervalSeconds = persisted.progressSummaryInterval
	}
	if os.Getenv("WECLAW_PROGRESS_MAX_MESSAGES") != "" && cfg.Progress.MaxProgressMessages == effective.progressMaxMessages {
		cfg.Progress.MaxProgressMessages = persisted.progressMaxMessages
	}
	if os.Getenv("WECLAW_PROGRESS_STREAM_TIMELINE_LIMIT") != "" && optionalIntEqual(cfg.Progress.StreamTimelineLimit, effective.progressStreamTimelineLimit) {
		cfg.Progress.StreamTimelineLimit = persisted.progressStreamTimelineLimit
	}
}

func optionalIntEqual(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// WithLockedSnapshot 在与配置写入相同的进程内和跨进程锁中读取最新配置。
// callback 只应用运行态快照，不能在其中再次调用 Save、Update 或 WithLockedSnapshot。
func WithLockedSnapshot(callback func(*Config) error) error {
	if callback == nil {
		return fmt.Errorf("config snapshot callback is nil")
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	return withConfigMutationLock(path, func() error {
		cfg, err := Load()
		if err != nil {
			return err
		}
		return callback(cfg)
	})
}

func withConfigMutationLock(path string, fn func() error) error {
	configMutationMu.Lock()
	defer configMutationMu.Unlock()
	if err := securefile.WithExclusiveLock(context.Background(), path+".lock", fn); err != nil {
		return fmt.Errorf("lock config: %w", err)
	}
	return nil
}

func saveConfigAtPath(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := securefile.Write(path, data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
