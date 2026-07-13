package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

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
		out:  &bytes.Buffer{},
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("runRestart error=%v, want %v", err, wantErr)
	}
}
