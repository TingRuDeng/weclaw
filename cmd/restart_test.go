package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunRestartStartsDirectlyWhenWeclawIsNotRunning(t *testing.T) {
	var out bytes.Buffer
	stopped := false
	started := false

	err := runRestart(context.Background(), false, restartOps{
		ensureSafe: func(context.Context, bool) error { return nil },
		isRunning:  func() bool { return false },
		stop: func() error {
			stopped = true
			return nil
		},
		start: func() error {
			started = true
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
		ensureSafe: func(_ context.Context, force bool) error {
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
		start: func() error {
			calls = append(calls, "start")
			return nil
		},
		out: &out,
	})

	if err != nil {
		t.Fatalf("runRestart error: %v", err)
	}
	want := []string{"safe", "running", "stop", "start"}
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
		ensureSafe: func(context.Context, bool) error { return wantErr },
		isRunning: func() bool {
			t.Fatal("安全检查失败后不应继续判断运行状态")
			return false
		},
		stop:  func() error { return nil },
		start: func() error { return nil },
		out:   &bytes.Buffer{},
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("runRestart error=%v, want %v", err, wantErr)
	}
}
