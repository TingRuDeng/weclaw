package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

// TestPrepareConfiguredStartDaemonChildSkipsCapabilityProbe 验证后台子进程不重复执行父进程已完成的 ACP 握手。
func TestPrepareConfiguredStartDaemonChildSkipsCapabilityProbe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	t.Setenv(daemonChildEnv, "1")
	t.Setenv("PATH", t.TempDir())
	marker := filepath.Join(home, "adapter-started")
	adapter := filepath.Join(home, "claude-agent-acp")
	script := "#!/bin/sh\n/usr/bin/touch '" + marker + "'\nexit 31\n"
	if err := os.WriteFile(adapter, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "acp", Command: adapter}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareConfiguredStart(context.Background(), func(*config.Config) error { return nil })
	if err != nil {
		t.Fatalf("prepareConfiguredStart error=%v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("daemon 子进程重复执行了 ACP adapter，stat error=%v", err)
	}
	if err := prepared.run(); err != nil {
		t.Fatalf("prepared.run error=%v", err)
	}
}

// TestConfiguredStartPreflightParentKeepsCapabilityProbe 验证父进程仍执行完整 ACP 能力握手。
func TestConfiguredStartPreflightParentKeepsCapabilityProbe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	t.Setenv(daemonChildEnv, "")
	t.Setenv("PATH", t.TempDir())
	marker := filepath.Join(home, "adapter-started")
	adapter := filepath.Join(home, "claude-agent-acp")
	script := "#!/bin/sh\n/usr/bin/touch '" + marker + "'\nexit 31\n"
	if err := os.WriteFile(adapter, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "acp", Command: adapter}
	err := configuredStartPreflight()(context.Background(), cfg)
	if err == nil {
		t.Fatal("父进程必须暴露 ACP 能力握手失败")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("父进程未执行 ACP adapter，stat error=%v", err)
	}
}

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
