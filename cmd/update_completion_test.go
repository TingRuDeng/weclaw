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
