package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

func TestRunRestartRejectsActiveControlledCLIWhileOffline(t *testing.T) {
	prepared := false
	err := runRestart(context.Background(), false, restartOps{
		acquireLease: func() (io.Closer, error) { return nil, agent.ErrCodexCLIFrontendActive },
		prepare: func(context.Context) (preparedStart, error) {
			prepared = true
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error { return nil },
		isRunning:  func() bool { return false },
		out:        &bytes.Buffer{},
	})
	if !errors.Is(err, agent.ErrCodexCLIFrontendActive) || !strings.Contains(err.Error(), "退出所有 weclaw codex cli") || !prepared {
		t.Fatalf("runRestart error=%v prepared=%v", err, prepared)
	}
}

// TestRunRestartDoesNotStopWhenPreflightFails 验证预检失败时旧服务保持运行。
func TestRunRestartDoesNotStopWhenPreflightFails(t *testing.T) {
	wantErr := errors.New("Claude 仅支持 ACP")
	stopped := false
	err := runRestart(context.Background(), false, restartOps{
		prepare: func(context.Context) (preparedStart, error) { return preparedStart{}, wantErr },
		ensureSafe: func(context.Context, bool, *config.Config) error {
			t.Fatal("预检失败后不应检查任务")
			return nil
		},
		isRunning: func() bool { t.Fatal("预检失败后不应检查进程"); return false },
		stop:      func() error { stopped = true; return nil },
		out:       &bytes.Buffer{},
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("runRestart error=%v, want %v", err, wantErr)
	}
	if stopped {
		t.Fatal("配置预检失败时不应停止旧服务")
	}
}

func TestRunRestartStartsDirectlyWhenWeclawIsNotRunning(t *testing.T) {
	var out bytes.Buffer
	stopped := false
	started := false

	err := runRestart(context.Background(), false, restartOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig(), run: func() error { started = true; return nil }}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error { return nil },
		isRunning:  func() bool { return false },
		stop: func() error {
			stopped = true
			return nil
		},
		out: &out,
	})

	if err != nil {
		t.Fatalf("runRestart error: %v", err)
	}
	if stopped {
		t.Fatal("未运行时 restart 不应执行停止流程")
	}
	if !started {
		t.Fatal("未运行时 restart 应直接启动")
	}
	if strings.Contains(out.String(), "正在停止 WeClaw") {
		t.Fatalf("output=%q，未运行时不应提示正在停止", out.String())
	}
	if !strings.Contains(out.String(), "未检测到运行中的 WeClaw，直接启动") {
		t.Fatalf("output=%q，缺少直接启动提示", out.String())
	}
}

func TestRunRestartDoesNotStartOfflineWhenCodexAppIsVisible(t *testing.T) {
	started := false
	err := runRestart(context.Background(), false, restartOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig(), run: func() error { started = true; return nil }}, nil
		},
		ensureSafe:  func(context.Context, bool, *config.Config) error { return nil },
		isRunning:   func() bool { return false },
		offlineSafe: func(*config.Config) error { return agent.ErrCodexDesktopFrontendActive },
		out:         &bytes.Buffer{},
	})
	if !errors.Is(err, agent.ErrCodexDesktopFrontendActive) || started {
		t.Fatalf("error=%v started=%v", err, started)
	}
}

func TestRunRestartStopsBeforeStartWhenWeclawIsRunning(t *testing.T) {
	var out bytes.Buffer
	var calls []string

	err := runRestart(context.Background(), true, restartOps{
		prepare: func(context.Context) (preparedStart, error) {
			calls = append(calls, "prepare")
			return preparedStart{cfg: config.DefaultConfig(), run: func() error { calls = append(calls, "start"); return nil }}, nil
		},
		ensureSafe: func(_ context.Context, force bool, _ *config.Config) error {
			if !force {
				t.Fatal("force flag 未传入安全检查")
			}
			calls = append(calls, "safe")
			return nil
		},
		isRunning: func() bool {
			calls = append(calls, "running")
			return true
		},
		stop: func() error {
			calls = append(calls, "stop")
			return nil
		},
		out: &out,
	})

	if err != nil {
		t.Fatalf("runRestart error: %v", err)
	}
	want := []string{"prepare", "safe", "running", "stop", "start"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls=%v, want %v", calls, want)
	}
	if !strings.Contains(out.String(), "正在停止 WeClaw") {
		t.Fatalf("output=%q，运行中应提示停止", out.String())
	}
}

func TestRunRestartStopsWhenSafetyCheckFails(t *testing.T) {
	wantErr := errors.New("安全检查失败")
	cancelled := false
	err := runRestart(context.Background(), false, restartOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig(), run: func() error { return nil }}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error { return wantErr },
		isRunning: func() bool {
			t.Fatal("安全检查失败后不应继续判断运行状态")
			return false
		},
		stop: func() error { return nil },
		cancelDrain: func(context.Context, *config.Config) error {
			cancelled = true
			return nil
		},
		out: &bytes.Buffer{},
	})

	if !errors.Is(err, wantErr) || !cancelled {
		t.Fatalf("runRestart error=%v cancelled=%v, want %v", err, cancelled, wantErr)
	}
}

func TestRunRestartDoesNotCancelUnsupportedLegacyTransaction(t *testing.T) {
	cancelled := false
	err := runRestart(context.Background(), false, restartOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error {
			return legacyRuntimeRestartError("v0.1.267")
		},
		isRunning: func() bool {
			t.Fatal("旧服务不支持协调端点时不应继续停止服务")
			return true
		},
		cancelDrain: func(context.Context, *config.Config) error {
			cancelled = true
			return nil
		},
		out: &bytes.Buffer{},
	})

	if !errors.Is(err, errCoordinatedRestartUnsupported) || cancelled {
		t.Fatalf("runRestart error=%v cancelled=%v", err, cancelled)
	}
}

func TestRunRestartDelegatesSystemdWithoutStartingPrivateDaemon(t *testing.T) {
	var calls []string
	err := runRestart(context.Background(), true, restartOps{
		prepare: func(context.Context) (preparedStart, error) {
			calls = append(calls, "prepare")
			return preparedStart{cfg: config.DefaultConfig(), run: func() error {
				t.Fatal("systemd restart must not start a private daemon")
				return nil
			}}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error {
			calls = append(calls, "drain")
			return nil
		},
		isRunning: func() bool { calls = append(calls, "running"); return true },
		isSystemd: func() bool { calls = append(calls, "systemd"); return true },
		restartSystemd: func() error {
			calls = append(calls, "restart-systemd")
			return nil
		},
		stop: func() error {
			t.Fatal("systemd restart must not signal and then spawn a private daemon")
			return nil
		},
		out: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("runRestart: %v", err)
	}
	want := "prepare,drain,running,systemd,restart-systemd"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls=%s, want %s", got, want)
	}
}

func TestRunRestartCancelsDrainWhenSystemdRestartFails(t *testing.T) {
	wantErr := errors.New("systemctl failed")
	cancelled := false
	err := runRestart(context.Background(), false, restartOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe:     func(context.Context, bool, *config.Config) error { return nil },
		isRunning:      func() bool { return true },
		isSystemd:      func() bool { return true },
		restartSystemd: func() error { return wantErr },
		cancelDrain:    func(context.Context, *config.Config) error { cancelled = true; return nil },
		out:            &bytes.Buffer{},
	})
	if !errors.Is(err, wantErr) || !cancelled {
		t.Fatalf("error=%v cancelled=%v, want failed supervisor restart to restore admission", err, cancelled)
	}
}

func TestRunRestartCancelsDrainWhenDirectStopFails(t *testing.T) {
	wantErr := errors.New("stop failed")
	cancelled := false
	err := runRestart(context.Background(), false, restartOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe:  func(context.Context, bool, *config.Config) error { return nil },
		isRunning:   func() bool { return true },
		isSystemd:   func() bool { return false },
		stop:        func() error { return wantErr },
		cancelDrain: func(context.Context, *config.Config) error { cancelled = true; return nil },
		out:         &bytes.Buffer{},
	})
	if !errors.Is(err, wantErr) || !cancelled {
		t.Fatalf("error=%v cancelled=%v, want failed stop to restore admission", err, cancelled)
	}
}

func TestRunRestartCancelsDrainWhenSystemdRestartIsUnavailable(t *testing.T) {
	cancelled := false
	err := runRestart(context.Background(), false, restartOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe:  func(context.Context, bool, *config.Config) error { return nil },
		isRunning:   func() bool { return true },
		isSystemd:   func() bool { return true },
		cancelDrain: func(context.Context, *config.Config) error { cancelled = true; return nil },
		out:         &bytes.Buffer{},
	})
	if err == nil || !cancelled {
		t.Fatalf("error=%v cancelled=%v, want unavailable supervisor to restore admission", err, cancelled)
	}
}
