package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestRunStopCoordinatesHostBeforeStoppingService(t *testing.T) {
	var calls []string
	var out bytes.Buffer
	cfg := config.DefaultConfig()
	err := runStop(context.Background(), stopOps{
		isRunning: func() bool {
			calls = append(calls, "running")
			return true
		},
		loadConfig: func() (*config.Config, error) {
			calls = append(calls, "load")
			return cfg, nil
		},
		prepare: func(_ context.Context, force bool, got *config.Config) error {
			if !force {
				t.Fatal("stop must preserve forced task draining before Host shutdown")
			}
			if got != cfg {
				t.Fatal("stop did not use the loaded runtime configuration")
			}
			calls = append(calls, "prepare")
			return nil
		},
		stop: func() error {
			calls = append(calls, "stop")
			return nil
		},
		cancel: func(context.Context, *config.Config) error {
			calls = append(calls, "cancel")
			return nil
		},
		out: &out,
	})

	if err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if got, want := strings.Join(calls, ","), "running,load,prepare,stop"; got != want {
		t.Fatalf("calls=%s, want %s", got, want)
	}
	if !strings.Contains(out.String(), "WeClaw 已停止") {
		t.Fatalf("output=%q, want stopped confirmation", out.String())
	}
}

func TestRunStopDoesNotStopServiceWhenHostPreparationFails(t *testing.T) {
	wantErr := errors.New("Codex Host 仍有活动任务")
	stopped := false
	cancelled := false
	err := runStop(context.Background(), stopOps{
		isRunning: func() bool { return true },
		loadConfig: func() (*config.Config, error) {
			return config.DefaultConfig(), nil
		},
		prepare: func(context.Context, bool, *config.Config) error { return wantErr },
		stop: func() error {
			stopped = true
			return nil
		},
		cancel: func(context.Context, *config.Config) error {
			cancelled = true
			return nil
		},
		out: &bytes.Buffer{},
	})

	if !errors.Is(err, wantErr) || stopped || !cancelled {
		t.Fatalf("error=%v stopped=%v cancelled=%v", err, stopped, cancelled)
	}
}

func TestRunStopRestoresHostWhenServiceStopFails(t *testing.T) {
	wantErr := errors.New("service did not exit")
	cancelled := false
	err := runStop(context.Background(), stopOps{
		isRunning: func() bool { return true },
		loadConfig: func() (*config.Config, error) {
			return config.DefaultConfig(), nil
		},
		prepare: func(context.Context, bool, *config.Config) error { return nil },
		stop:    func() error { return wantErr },
		cancel: func(context.Context, *config.Config) error {
			cancelled = true
			return nil
		},
		out: &bytes.Buffer{},
	})

	if !errors.Is(err, wantErr) || !cancelled {
		t.Fatalf("error=%v cancelled=%v", err, cancelled)
	}
}

func TestRunStopDoesNotCompensateUnsupportedRuntime(t *testing.T) {
	stopped := false
	cancelled := false
	err := runStop(context.Background(), stopOps{
		isRunning: func() bool { return true },
		loadConfig: func() (*config.Config, error) {
			return config.DefaultConfig(), nil
		},
		prepare: func(context.Context, bool, *config.Config) error {
			return errCoordinatedRestartUnsupported
		},
		stop: func() error {
			stopped = true
			return nil
		},
		cancel: func(context.Context, *config.Config) error {
			cancelled = true
			return nil
		},
		out: &bytes.Buffer{},
	})

	if !errors.Is(err, errCoordinatedRestartUnsupported) || stopped || cancelled {
		t.Fatalf("error=%v stopped=%v cancelled=%v", err, stopped, cancelled)
	}
}

func TestRunStopSkipsHostPreparationWhenServiceIsNotRunning(t *testing.T) {
	stopped := false
	err := runStop(context.Background(), stopOps{
		isRunning: func() bool { return false },
		loadConfig: func() (*config.Config, error) {
			t.Fatal("stopped service must not require runtime configuration")
			return nil, nil
		},
		prepare: func(context.Context, bool, *config.Config) error {
			t.Fatal("stopped service cannot run the loopback Host transaction")
			return nil
		},
		stop: func() error {
			stopped = true
			return nil
		},
		out: &bytes.Buffer{},
	})

	if err != nil || !stopped {
		t.Fatalf("runStop error=%v stopped=%v", err, stopped)
	}
}
