package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
)

func TestCommandsRefuseUnsupportedLegacyRuntimeEvenWithForce(t *testing.T) {
	for _, command := range []string{"restart", "stop", "update"} {
		t.Run(command, func(t *testing.T) {
			cfg := config.DefaultConfig()
			unsupported := legacyRuntimeRestartError("v0.1.245")
			prepared := preparedStart{cfg: cfg, run: func() error {
				t.Fatal("不支持协调接口时不得启动新服务")
				return nil
			}}
			prepare := func(context.Context) (preparedStart, error) { return prepared, nil }
			ensureSafe := func(_ context.Context, force bool, _ *config.Config) error {
				if !force {
					t.Fatal("缺少强制授权")
				}
				return unsupported
			}
			cancel := func(context.Context, *config.Config) error { t.Fatal("不应补偿不存在的旧接口"); return nil }
			stop := func() error { t.Fatal("不支持协调接口时不得停止服务"); return nil }
			rolledBack := false
			var err error
			switch command {
			case "restart":
				err = runRestart(context.Background(), true, restartOps{
					prepare: prepare, ensureSafe: ensureSafe, stop: stop, cancelDrain: cancel, out: &bytes.Buffer{},
				})
			case "stop":
				err = runStopWithOptions(context.Background(), true, stopOps{
					loadConfig: func() (*config.Config, error) { return cfg, nil },
					isRunning:  func() bool { return true },
					prepare:    ensureSafe, stop: stop, cancel: cancel, out: &bytes.Buffer{},
				})
			case "update":
				err = completeUpdateWithRollback(context.Background(), true, true, updateCompletionOps{
					prepare: prepare, ensureSafe: ensureSafe, stop: stop, cancelDrain: cancel, out: &bytes.Buffer{},
				}, func() error {
					rolledBack = true
					return nil
				})
			}
			if !errors.Is(err, errCoordinatedRestartUnsupported) {
				t.Fatalf("err=%v, want errCoordinatedRestartUnsupported", err)
			}
			if command == "update" && !rolledBack {
				t.Fatal("update 遇到 404 时应回滚新二进制")
			}
		})
	}
}

func TestOfflineForceUsesConfiguredCodexAuthority(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	candidate := config.AgentConfig{Command: "codex", Args: []string{"app-server"}}
	cfg.Agents = map[string]config.AgentConfig{"primary": candidate, "secondary": candidate}
	if _, err := configuredOfflineCodexController(context.Background(), cfg); err == nil {
		t.Fatal("多个无明确权威的 Codex Agent 必须拒绝")
	}
	cfg.Agents["codex"] = candidate
	if controller, err := configuredOfflineCodexController(context.Background(), cfg); err != nil || controller == nil {
		t.Fatalf("controller=%v err=%v", controller, err)
	}
}

func TestOfflineRecoveryWaitAllowsSystemdStartupDelay(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	if err := messaging.PrepareOfflineRuntimeRestart(context.Background(), func() error { return nil }, nil, "systemd"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- waitOfflineRestartRecovery(ctx) }()
	// Type=simple 的 systemctl 可以早于运行态写入返回。
	time.Sleep(150 * time.Millisecond)
	lock, err := acquireRuntimeLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Version: Version, Mode: "systemd"}); err != nil {
		t.Fatal(err)
	}
	if err := messaging.NewHandler(nil, nil).RecoverRuntimeRestart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOfflineForceRetryUsesRecordedSystemd(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	if err := messaging.PrepareOfflineRuntimeRestart(context.Background(), func() error { return nil }, nil, "systemd"); err != nil {
		t.Fatal(err)
	}
	lease, err := agent.AcquireCodexRestartLease()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	want := errors.New("测试在启动边界停止")
	err = offlineForceRuntimeWithLease(context.Background(), config.DefaultConfig(), func(systemd bool) error {
		if !systemd {
			t.Fatal("systemd 重试不应启动私有 daemon")
		}
		return want
	}, func(context.Context, *config.Config) (offlineCodexController, error) { return nil, nil })
	if !errors.Is(err, want) || !errors.Is(err, messaging.ErrOfflineRuntimeRestartIncomplete) {
		t.Fatalf("err=%v", err)
	}
}

func TestOfflineForceRechecksMissingCodexConfigAfterLock(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	lease, err := agent.AcquireCodexRestartLease()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	want := errors.New("服务停止前刚刚创建了 Host")
	checks := 0
	err = offlineForceRuntimeWithLease(context.Background(), config.DefaultConfig(), func(bool) error {
		t.Fatal("仍有未处理 Host 时不得启动")
		return nil
	}, func(context.Context, *config.Config) (offlineCodexController, error) {
		checks++
		if checks == 2 {
			return nil, want
		}
		return nil, nil
	})
	if !errors.Is(err, want) || !errors.Is(err, messaging.ErrOfflineRuntimeRestartIncomplete) {
		t.Fatalf("err=%v", err)
	}
	if err := messaging.NewHandler(nil, nil).RecoverRuntimeRestart(context.Background()); err == nil {
		t.Fatal("未完成的停止记录不得开放启动")
	}
}
