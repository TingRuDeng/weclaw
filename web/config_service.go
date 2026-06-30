package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
)

// configService 负责配置的读取(脱敏)/校验/原子写回。
type configService struct {
	load func() (*config.Config, error)
	save func(*config.Config) error
}

func newConfigService() *configService {
	return &configService{
		load: config.Load,
		save: atomicSaveConfig,
	}
}

// view 返回当前配置的脱敏视图。
func (s *configService) view() (configView, error) {
	cfg, err := s.load()
	if err != nil {
		return configView{}, err
	}
	return redactConfig(cfg), nil
}

// apply 合并脱敏视图、校验并原子写回；返回是否需要重启。
func (s *configService) apply(v configView) (restartRequired bool, err error) {
	current, err := s.load()
	if err != nil {
		return false, err
	}
	merged := mergeView(current, v)
	if err := validateConfig(merged); err != nil {
		return false, err
	}
	restartRequired = platformTopologyChanged(current, merged)
	if err := s.save(merged); err != nil {
		return false, err
	}
	return restartRequired, nil
}

// validateConfig 做保存前的基本校验，避免写入明显非法的配置。
func validateConfig(cfg *config.Config) error {
	for name, ag := range cfg.Agents {
		if strings.TrimSpace(ag.Type) == "" {
			return fmt.Errorf("agent %q: type is required", name)
		}
		switch ag.Type {
		case "http":
			if strings.TrimSpace(ag.Endpoint) == "" {
				return fmt.Errorf("agent %q: http type requires endpoint", name)
			}
		case "cli", "acp", "companion":
			if strings.TrimSpace(ag.Command) == "" {
				return fmt.Errorf("agent %q: %s type requires command", name, ag.Type)
			}
		default:
			return fmt.Errorf("agent %q: unknown type %q", name, ag.Type)
		}
	}
	if cfg.RateLimitPerMinute < 0 {
		return fmt.Errorf("rate_limit_per_minute must be >= 0")
	}
	return nil
}

// atomicSaveConfig 通过临时文件 + rename 原子写回 config.json(0600)，失败不破坏原文件。
func atomicSaveConfig(cfg *config.Config) error {
	path, err := config.ConfigPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后该文件已不存在，Remove 无害
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
