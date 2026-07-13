package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

// TestPrepareStartRejectsLoadAndPreflightErrors 验证启动准备阶段不会把错误延迟到停服之后。
func TestPrepareStartRejectsLoadAndPreflightErrors(t *testing.T) {
	wantLoadErr := errors.New("加载失败")
	_, err := prepareStart(context.Background(), startPreparationOps{
		loadConfig: func() (*config.Config, error) { return nil, wantLoadErr },
	})
	if !errors.Is(err, wantLoadErr) {
		t.Fatalf("load error=%v, want %v", err, wantLoadErr)
	}
	wantPreflightErr := errors.New("能力缺失")
	_, err = prepareStart(context.Background(), startPreparationOps{
		loadConfig: func() (*config.Config, error) { return config.DefaultConfig(), nil },
		preflight:  func(context.Context, *config.Config) error { return wantPreflightErr },
	})
	if !errors.Is(err, wantPreflightErr) {
		t.Fatalf("preflight error=%v, want %v", err, wantPreflightErr)
	}
}

// TestPersistDetectedStartConfigSkipsUnchangedConfig 验证无自动探测变更时不触碰配置文件。
func TestPersistDetectedStartConfigSkipsUnchangedConfig(t *testing.T) {
	called := false
	err := persistDetectedStartConfig(false, config.DefaultConfig(), func(*config.Config) error {
		called = true
		return nil
	})
	if err != nil || called {
		t.Fatalf("error=%v called=%t, want no save", err, called)
	}
}

// TestPersistDetectedStartConfigSavesChangedConfig 验证自动探测配置在启动前持久化。
func TestPersistDetectedStartConfigSavesChangedConfig(t *testing.T) {
	wantCfg := config.DefaultConfig()
	called := false
	err := persistDetectedStartConfig(true, wantCfg, func(got *config.Config) error {
		called = true
		if got != wantCfg {
			t.Fatal("保存时未使用预检配置快照")
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("error=%v called=%t, want successful save", err, called)
	}
}
