package web

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
)

// configService 负责配置的读取(脱敏)/校验/原子写回。
type configService struct {
	load   func() (*config.Config, error)
	save   func(*config.Config) error
	update func(func(*config.Config) error) error
}

func newConfigService() *configService {
	return &configService{
		load:   config.Load,
		save:   atomicSaveConfig,
		update: config.Update,
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
	if s.update != nil {
		err := s.update(func(current *config.Config) error {
			if err := validateConfigViewRevision(current, v); err != nil {
				return err
			}
			merged := mergeView(current, v)
			if err := validateConfig(merged); err != nil {
				return err
			}
			restartRequired = restartRequiredConfigChanged(current, merged)
			*current = *merged
			return nil
		})
		return restartRequired, err
	}
	current, err := s.load()
	if err != nil {
		return false, err
	}
	if err := validateConfigViewRevision(current, v); err != nil {
		return false, err
	}
	merged := mergeView(current, v)
	if err := validateConfig(merged); err != nil {
		return false, err
	}
	restartRequired = restartRequiredConfigChanged(current, merged)
	if err := s.save(merged); err != nil {
		return false, err
	}
	return restartRequired, nil
}

func validateConfigViewRevision(current *config.Config, view configView) error {
	if strings.TrimSpace(view.Revision) == "" || view.Revision != configRevision(current) {
		return fmt.Errorf("配置已变化，请重新加载后再保存")
	}
	return nil
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
	if err := cfg.ValidateClaudeACPAgents(); err != nil {
		return err
	}
	return cfg.Validate()
}

// atomicSaveConfig 通过临时文件 + rename 原子写回 config.json(0600)，失败不破坏原文件。
func atomicSaveConfig(cfg *config.Config) error {
	return config.Save(cfg)
}
