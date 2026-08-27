package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func TestStopRegistersForceCodexTermination(t *testing.T) {
	flag := stopCmd.Flags().Lookup("force")
	if flag == nil || !strings.Contains(flag.Usage, "关闭 Codex App") || !strings.Contains(flag.Usage, "Codex Host") {
		t.Fatalf("stop force flag=%v, want Codex termination contract", flag)
	}
}

func TestRunStopForceDoesNotBypassCodexTerminationFailure(t *testing.T) {
	wantErr := errors.New("Codex Host 进程身份无法确认")
	var calls []string
	err := runStopWithOptions(context.Background(), true, stopOps{
		isRunning: func() bool { calls = append(calls, "running"); return true },
		loadConfig: func() (*config.Config, error) {
			calls = append(calls, "load")
			return config.DefaultConfig(), nil
		},
		prepare: func(context.Context, bool, *config.Config) error {
			calls = append(calls, "prepare")
			return wantErr
		},
		cancel: func(context.Context, *config.Config) error {
			calls = append(calls, "cancel")
			return nil
		},
		stop: func() error { calls = append(calls, "stop"); return nil },
		out:  &bytes.Buffer{},
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("runStopWithOptions force error=%v, want termination failure", err)
	}
	if got, want := strings.Join(calls, ","), "running,load,prepare,cancel"; got != want {
		t.Fatalf("calls=%s, want %s", got, want)
	}
}

func TestRunStopForcePropagatesCodexTerminationAuthorization(t *testing.T) {
	called := false
	err := runStopWithOptions(context.Background(), true, stopOps{
		isRunning:  func() bool { return true },
		loadConfig: func() (*config.Config, error) { return config.DefaultConfig(), nil },
		prepareWithOptions: func(_ context.Context, forceDrain bool, forceTerminate bool, _ *config.Config) error {
			called = true
			if !forceDrain || !forceTerminate {
				t.Fatalf("forceDrain=%v forceTerminate=%v", forceDrain, forceTerminate)
			}
			return nil
		},
		stop: func() error { return nil },
		out:  &bytes.Buffer{},
	})
	if err != nil || !called {
		t.Fatalf("runStopWithOptions error=%v called=%v", err, called)
	}
}

func TestRunStopForceTerminatesOfflineCodexBeforeStopping(t *testing.T) {
	var calls []string
	err := runStopWithOptions(context.Background(), true, stopOps{
		isRunning: func() bool { calls = append(calls, "running"); return false },
		loadConfig: func() (*config.Config, error) {
			calls = append(calls, "load")
			return config.DefaultConfig(), nil
		},
		acquireLease: func() (io.Closer, error) {
			calls = append(calls, "lease")
			return io.NopCloser(strings.NewReader("")), nil
		},
		offlineForce: func(*config.Config) error {
			calls = append(calls, "force")
			return nil
		},
		stop: func() error { calls = append(calls, "stop"); return nil },
		out:  &bytes.Buffer{},
	})

	if err != nil {
		t.Fatalf("runStopWithOptions force offline: %v", err)
	}
	if got, want := strings.Join(calls, ","), "running,load,lease,force,stop"; got != want {
		t.Fatalf("calls=%s, want %s", got, want)
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

func TestRunStopMigratesUnsupportedRuntimeAfterExplicitLegacyStop(t *testing.T) {
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
		legacyStop: func(context.Context, *config.Config, func() error) error {
			stopped = true
			return nil
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

	if err != nil || !stopped || cancelled {
		t.Fatalf("error=%v stopped=%v cancelled=%v", err, stopped, cancelled)
	}
}

func TestRunStopForceRefusesLegacyRuntimeWithoutCodexTransaction(t *testing.T) {
	legacyStopped := false
	serviceStopped := false
	err := runStopWithOptions(context.Background(), true, stopOps{
		isRunning:  func() bool { return true },
		loadConfig: func() (*config.Config, error) { return config.DefaultConfig(), nil },
		prepareWithOptions: func(context.Context, bool, bool, *config.Config) error {
			return errCoordinatedRestartUnsupported
		},
		legacyStop: func(context.Context, *config.Config, func() error) error {
			legacyStopped = true
			return nil
		},
		stop: func() error {
			serviceStopped = true
			return nil
		},
		out: &bytes.Buffer{},
	})

	if !errors.Is(err, errCoordinatedRestartUnsupported) {
		t.Fatalf("error=%v, want unsupported coordinated force", err)
	}
	if legacyStopped || serviceStopped {
		t.Fatalf("legacyStopped=%v serviceStopped=%v, force must not degrade to service-only stop", legacyStopped, serviceStopped)
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
