package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

// TestCompleteUpdateWithoutRestartDoesNotRunPreparedStart 验证普通更新只预检，不启动服务。
func TestCompleteUpdateWithoutRestartDoesNotRunPreparedStart(t *testing.T) {
	started := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig(), run: func() error { started = true; return nil }}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error { return nil },
		running:    func() bool { return true },
		stop:       func() error { return nil },
		out:        &bytes.Buffer{},
	}
	if err := completeUpdate(context.Background(), false, false, ops); err != nil {
		t.Fatalf("completeUpdate error=%v", err)
	}
	if started {
		t.Fatal("普通更新不应启动服务")
	}
}

// TestCompleteUpdateRestartUsesValidatedStart 验证更新后重启严格按安全检查、停止、启动执行。
func TestCompleteUpdateRestartUsesValidatedStart(t *testing.T) {
	var calls []string
	cfg := config.DefaultConfig()
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			calls = append(calls, "prepare")
			return preparedStart{cfg: cfg, run: func() error { calls = append(calls, "start"); return nil }}, nil
		},
		ensureSafe: func(_ context.Context, force bool, got *config.Config) error {
			if !force || got != cfg {
				t.Fatal("安全检查未收到 force 或同一配置快照")
			}
			calls = append(calls, "safe")
			return nil
		},
		running: func() bool { calls = append(calls, "running"); return true },
		stop:    func() error { calls = append(calls, "stop"); return nil },
		out:     &bytes.Buffer{},
	}
	if err := completeUpdate(context.Background(), true, true, ops); err != nil {
		t.Fatalf("completeUpdate error=%v", err)
	}
	want := []string{"prepare", "safe", "running", "stop", "start"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v, want %v", calls, want)
	}
}

// TestCompleteUpdateSafetyFailureKeepsOldService 验证任务安全检查失败时不会停止旧服务。
func TestCompleteUpdateSafetyFailureKeepsOldService(t *testing.T) {
	wantErr := errors.New("存在运行中任务")
	stopped := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig(), run: func() error { return nil }}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error { return wantErr },
		running:    func() bool { t.Fatal("安全检查失败后不应读取运行状态"); return false },
		stop:       func() error { stopped = true; return nil },
		out:        &bytes.Buffer{},
	}
	err := completeUpdate(context.Background(), true, false, ops)
	if !errors.Is(err, wantErr) || stopped {
		t.Fatalf("error=%v stopped=%t, want safety failure without stop", err, stopped)
	}
}

func TestCompleteUpdateDoesNotCancelUnsupportedLegacyTransaction(t *testing.T) {
	cancelled := false
	rolledBack := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error {
			return legacyRuntimeRestartError("v0.1.267")
		},
		cancelDrain: func(context.Context, *config.Config) error {
			cancelled = true
			return nil
		},
		out: &bytes.Buffer{},
	}

	err := completeUpdateWithRollback(context.Background(), true, false, ops, func() error {
		rolledBack = true
		return nil
	})

	if !errors.Is(err, errCoordinatedRestartUnsupported) || cancelled || !rolledBack {
		t.Fatalf("completeUpdate error=%v cancelled=%v rolledBack=%v", err, cancelled, rolledBack)
	}
}

// TestRestartUpdatedServiceDoesNotStartStoppedService 验证更新前未运行时不会意外启动服务。
func TestRestartUpdatedServiceDoesNotStartStoppedService(t *testing.T) {
	started := false
	prepared := preparedStart{run: func() error { started = true; return nil }}
	ops := updateCompletionOps{
		running: func() bool { return false },
		stop:    func() error { t.Fatal("未运行时不应停止"); return nil },
		out:     &bytes.Buffer{},
	}
	if err := restartUpdatedService(prepared, ops); err != nil {
		t.Fatalf("restartUpdatedService error=%v", err)
	}
	if started {
		t.Fatal("更新前未运行时不应自动启动")
	}
}

// TestRestartUpdatedServiceReturnsStartError 验证已停止旧服务后启动失败会显式返回。
func TestRestartUpdatedServiceReturnsStartError(t *testing.T) {
	wantErr := errors.New("启动失败")
	prepared := preparedStart{run: func() error { return wantErr }}
	ops := updateCompletionOps{
		running: func() bool { return true },
		stop:    func() error { return nil },
		out:     &bytes.Buffer{},
	}
	err := restartUpdatedService(prepared, ops)
	if !errors.Is(err, wantErr) {
		t.Fatalf("restartUpdatedService error=%v, want %v", err, wantErr)
	}
}

func TestFinishUpdateRestartFailureRestoresPreviousVersionAndService(t *testing.T) {
	wantErr := errors.New("new version failed to start")
	var calls []string
	running := true
	rolledBack := false
	committed := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			calls = append(calls, "prepare")
			return preparedStart{
				cfg: config.DefaultConfig(),
				run: func() error {
					if !rolledBack {
						calls = append(calls, "start-new")
						return wantErr
					}
					calls = append(calls, "start-old")
					running = true
					return nil
				},
			}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error {
			calls = append(calls, "safe")
			return nil
		},
		running: func() bool {
			calls = append(calls, "running")
			return running
		},
		stop: func() error {
			calls = append(calls, "stop")
			running = false
			return nil
		},
		out: &bytes.Buffer{},
	}
	transaction := updateTransaction{
		commit: func() { committed = true },
		rollback: func() error {
			calls = append(calls, "rollback")
			rolledBack = true
			return nil
		},
	}

	err := finishUpdate(
		context.Background(), "v1", "v2", true, false,
		func(string) (updateTransaction, error) {
			calls = append(calls, "apply")
			return transaction, nil
		},
		ops,
		&bytes.Buffer{},
	)

	if !errors.Is(err, wantErr) {
		t.Fatalf("finishUpdate error=%v, want %v", err, wantErr)
	}
	wantCalls := []string{"apply", "prepare", "safe", "running", "stop", "start-new", "rollback", "running", "start-old"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls=%v, want %v", calls, wantCalls)
	}
	if !rolledBack || committed || !running {
		t.Fatalf("rolledBack=%t committed=%t running=%t, want recovered old service", rolledBack, committed, running)
	}
}

func TestFinishUpdatePreflightFailureRestoresPreviousBinary(t *testing.T) {
	wantErr := errors.New("preflight failed")
	rolledBack := false
	stopped := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) { return preparedStart{}, wantErr },
		ensureSafe: func(context.Context, bool, *config.Config) error {
			t.Fatal("preflight failure must stop before safety check")
			return nil
		},
		running: func() bool { t.Fatal("preflight failure must not inspect service"); return false },
		stop:    func() error { stopped = true; return nil },
		out:     &bytes.Buffer{},
	}

	err := finishUpdate(
		context.Background(), "v1", "v2", true, false,
		func(string) (updateTransaction, error) {
			return updateTransaction{rollback: func() error { rolledBack = true; return nil }}, nil
		},
		ops,
		&bytes.Buffer{},
	)

	if !errors.Is(err, wantErr) || !rolledBack || stopped {
		t.Fatalf("error=%v rolledBack=%t stopped=%t, want rollback before stop", err, rolledBack, stopped)
	}
}

func TestFinishUpdateWithoutRestartStillRollsBackPreflightFailure(t *testing.T) {
	wantErr := errors.New("preflight failed")
	rolledBack := false
	committed := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) { return preparedStart{}, wantErr },
		ensureSafe: func(context.Context, bool, *config.Config) error {
			t.Fatal("preflight failure must stop before safety check")
			return nil
		},
		running: func() bool { t.Fatal("preflight failure must not inspect service"); return false },
		stop:    func() error { t.Fatal("preflight failure must not stop service"); return nil },
		out:     &bytes.Buffer{},
	}

	err := finishUpdate(
		context.Background(), "v1", "v2", false, false,
		func(string) (updateTransaction, error) {
			return updateTransaction{
				commit:   func() { committed = true },
				rollback: func() error { rolledBack = true; return nil },
			}, nil
		},
		ops,
		&bytes.Buffer{},
	)

	if !errors.Is(err, wantErr) || !rolledBack || committed {
		t.Fatalf("error=%v rolledBack=%t committed=%t, want rollback without commit", err, rolledBack, committed)
	}
}
