//go:build darwin || linux

package cmd

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
)

func TestLegacyForceStopsIsolated404Service(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECLAW_HOME", dir)
	exe := filepath.Join(t.TempDir(), "weclaw-legacy-fixture")
	build := exec.Command("go", "build", "-o", exe, "./testdata/legacy_runtime/main.go")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	child := exec.Command(exe, "start", "-f")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("隔离服务未退出")
		}
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	cfg := config.DefaultConfig()
	cfg.Agents = map[string]config.AgentConfig{}
	select {
	case cfg.APIAddr = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("隔离服务启动超时")
	}
	if cfg.APIAddr == "" {
		t.Fatal("隔离服务未返回地址")
	}
	cause := beginRestartDrainWithConfigOptions(context.Background(), true, cfg)
	var legacy *legacyRuntimeError
	if !errors.As(cause, &legacy) || legacy.state.Version != "v0.1.245" {
		t.Fatalf("cause=%v", cause)
	}
	// 受控 CLI 租约必须在停止旧服务前检查。
	lease, err := agent.AcquireCodexCLIFrontendLease()
	if err != nil {
		t.Fatal(err)
	}
	if err := forceLegacyRuntime(context.Background(), cfg, cause, nil); err == nil {
		t.Fatal("活动 CLI 租约不能被绕过")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if !processExists(child.Process.Pid) {
		t.Fatal("租约失败不应停止服务")
	}
	// 404 后运行记录被另一实例改写时，不能向旧 PID 发信号。
	changed := legacy.state
	changed.Version = "changed"
	if err := writeRuntimeState(changed); err != nil {
		t.Fatal(err)
	}
	if err := forceLegacyRuntime(context.Background(), cfg, cause, nil); err == nil {
		t.Fatal("必须拒绝变更后的运行记录")
	}
	if !processExists(child.Process.Pid) {
		t.Fatal("身份变化不应停止服务")
	}
	if err := writeRuntimeState(legacy.state); err != nil {
		t.Fatal(err)
	}
	lease, err = agent.AcquireCodexRestartLease()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	// 只替换 Codex 边界，进程核验、真实信号、运行锁与 journal 均走正式实现。
	noCodex := func(context.Context, *config.Config) (legacyCodexController, error) { return nil, nil }
	if err := forceLegacyRuntimeWithLease(context.Background(), cfg, cause, nil, noCodex); err != nil {
		t.Fatal(err)
	}
	if processExists(child.Process.Pid) {
		t.Fatal("强制迁移成功后旧进程仍在运行")
	}
	if pending, err := messaging.RuntimeRestartPending(); err != nil || !pending {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	// 新的离线 force 可以重入；普通启动通过恢复器消费已完成的停止记录。
	if err := forceLegacyRuntimeWithLease(context.Background(), cfg, nil, nil, noCodex); err != nil {
		t.Fatal(err)
	}
	if err := messaging.NewHandler(nil, nil).RecoverRuntimeRestart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pending, err := messaging.RuntimeRestartPending(); err != nil || pending {
		t.Fatalf("pending=%v err=%v", pending, err)
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

func TestLegacyForceOfflineRetryUsesRecordedSystemd(t *testing.T) {
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
	err = forceLegacyRuntimeWithLease(context.Background(), config.DefaultConfig(), nil, func(systemd bool) error {
		if !systemd {
			t.Fatal("systemd 重试不应启动私有 daemon")
		}
		return want
	}, func(context.Context, *config.Config) (legacyCodexController, error) { return nil, nil })
	if !errors.Is(err, want) || !errors.Is(err, messaging.ErrOfflineRuntimeRestartIncomplete) {
		t.Fatalf("err=%v", err)
	}
}

func TestLegacyForceRechecksMissingCodexConfigAfterServiceExit(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	lease, err := agent.AcquireCodexRestartLease()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	want := errors.New("旧服务停止前刚刚创建了 Host")
	checks := 0
	err = forceLegacyRuntimeWithLease(context.Background(), config.DefaultConfig(), nil, func(bool) error {
		t.Fatal("仍有未处理 Host 时不得启动")
		return nil
	}, func(context.Context, *config.Config) (legacyCodexController, error) {
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
