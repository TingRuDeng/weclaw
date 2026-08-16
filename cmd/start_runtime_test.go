package cmd

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type shutdownDrainStub struct {
	drained *atomic.Bool
}

type coordinatedShutdownStub struct {
	prepared   *atomic.Bool
	drained    *atomic.Bool
	forced     *atomic.Bool
	prepareErr error
}

func TestStartHandlerStatusShowsWeClawVersion(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	previousVersion := Version
	Version = "v9.8.7"
	t.Cleanup(func() { Version = previousVersion })

	handler := newStartHandlerWithTrace(config.DefaultConfig(), nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	handler.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "main",
		UserID:    "ou_user",
		ChatID:    "oc_chat",
		MessageID: "status-version",
		Text:      "/status",
	}, reply)

	text := strings.Join(reply.TextsSnapshot(), "\n")
	if !strings.Contains(text, "version: v9.8.7") {
		t.Fatalf("/status reply missing build version, got %q", text)
	}
}

func (s shutdownDrainStub) Drain(context.Context, bool) (messaging.RuntimeDrainResult, error) {
	s.drained.Store(true)
	return messaging.RuntimeDrainResult{ActiveTasks: 1}, nil
}

func (s coordinatedShutdownStub) Drain(context.Context, bool) (messaging.RuntimeDrainResult, error) {
	if s.drained != nil {
		s.drained.Store(true)
	}
	return messaging.RuntimeDrainResult{}, nil
}

func (s coordinatedShutdownStub) PrepareRuntimeRestart(_ context.Context, force bool) (messaging.RuntimeRestartResult, error) {
	if s.prepared != nil {
		s.prepared.Store(true)
	}
	if s.forced != nil {
		s.forced.Store(force)
	}
	return messaging.RuntimeRestartResult{Codex: true}, s.prepareErr
}

func TestRunUntilShutdownDrainsTasksBeforeCancellingPlatforms(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var terminalized atomic.Bool
	platformStopped := make(chan bool, 1)
	runtime := startRuntime{
		ctx: ctx, cancel: cancel, drain: shutdownDrainStub{drained: &terminalized}, shutdownTimeout: time.Second,
		runBridgeFn: func() error {
			<-ctx.Done()
			platformStopped <- terminalized.Load()
			return nil
		},
	}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	if err := runtime.runUntilShutdown(signals); err != nil {
		t.Fatalf("runUntilShutdown: %v", err)
	}
	if drainedFirst := <-platformStopped; !drainedFirst {
		t.Fatal("platform context was cancelled before active task terminalized")
	}
}

func TestRunUntilShutdownCoordinatesCodexHostBeforeCancellingPlatforms(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var prepared atomic.Bool
	var drained atomic.Bool
	var forced atomic.Bool
	platformStopped := make(chan bool, 1)
	runtime := startRuntime{
		ctx:             ctx,
		cancel:          cancel,
		drain:           coordinatedShutdownStub{prepared: &prepared, drained: &drained, forced: &forced},
		shutdownTimeout: time.Second,
		runBridgeFn: func() error {
			<-ctx.Done()
			platformStopped <- prepared.Load()
			return nil
		},
	}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM

	if err := runtime.runUntilShutdown(signals); err != nil {
		t.Fatalf("runUntilShutdown: %v", err)
	}
	if preparedFirst := <-platformStopped; !preparedFirst {
		t.Fatal("platform context was cancelled before coordinated Codex Host shutdown")
	}
	if drained.Load() {
		t.Fatal("signal shutdown used generic task drain instead of the coordinated Host transaction")
	}
	if !forced.Load() {
		t.Fatal("external signal shutdown must preserve the existing forced task-drain behavior")
	}
}

func TestRunUntilShutdownPreservesHostAndStopsPlatformsWhenCoordinationFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var prepared atomic.Bool
	var drained atomic.Bool
	platformStopped := make(chan bool, 1)
	wantErr := errors.New("Host still has an active thread")
	runtime := startRuntime{
		ctx: ctx, cancel: cancel,
		drain:           coordinatedShutdownStub{prepared: &prepared, drained: &drained, prepareErr: wantErr},
		shutdownTimeout: time.Second,
		runBridgeFn: func() error {
			<-ctx.Done()
			platformStopped <- prepared.Load()
			return nil
		},
	}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM

	var logs bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })

	if err := runtime.runUntilShutdown(signals); err != nil {
		t.Fatalf("runUntilShutdown: %v", err)
	}
	if preparedFirst := <-platformStopped; !preparedFirst {
		t.Fatal("platform context was cancelled before failed Host coordination completed")
	}
	if drained.Load() {
		t.Fatal("failed coordinated shutdown must not fall back to generic task drain")
	}
	if got := logs.String(); !strings.Contains(got, "preserving Host") || !strings.Contains(got, wantErr.Error()) {
		t.Fatalf("shutdown log=%q, want preserved Host reason", got)
	}
}

func TestRunUntilShutdownCoordinatesCodexHostWhenBridgeStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var prepared atomic.Bool
	var forced atomic.Bool
	wantErr := errors.New("message bridge stopped")
	runtime := startRuntime{
		ctx: ctx, cancel: cancel,
		drain:           coordinatedShutdownStub{prepared: &prepared, forced: &forced},
		shutdownTimeout: time.Second,
		runBridgeFn:     func() error { return wantErr },
	}

	if err := runtime.runUntilShutdown(make(chan os.Signal)); !errors.Is(err, wantErr) {
		t.Fatalf("runUntilShutdown error=%v, want %v", err, wantErr)
	}
	if !prepared.Load() || !forced.Load() {
		t.Fatalf("bridge exit skipped coordinated shutdown: prepared=%v forced=%v", prepared.Load(), forced.Load())
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("bridge exit did not cancel the runtime context")
	}
}

func TestSystemdServiceSignalsOnlyWeClawMainProcess(t *testing.T) {
	unit, err := os.ReadFile(filepath.Join("..", "service", "weclaw.service"))
	if err != nil {
		t.Fatalf("read systemd service: %v", err)
	}
	text := string(unit)
	if !strings.Contains(text, "KillMode=process") {
		t.Fatal("systemd service must signal only the WeClaw main process so it can coordinate the Codex Host")
	}
}

func TestStopAllWeclawRemovesPidFileAfterProcessExit(t *testing.T) {
	exists := true
	removed := false
	var signals []syscall.Signal

	err := stopAllWeclawWithOps(stopProcessOps{
		readPid: func() (int, error) { return 1234, nil },
		processExists: func(pid int) bool {
			if pid != 1234 {
				t.Fatalf("processExists pid=%d, want 1234", pid)
			}
			return exists
		},
		signalPID: func(pid int, sig syscall.Signal) error {
			signals = append(signals, sig)
			if sig == syscall.SIGTERM {
				exists = false
			}
			return nil
		},
		signalProcessGroup: func(int, syscall.Signal) error { return nil },
		removePIDFile: func() error {
			if exists {
				t.Fatal("进程仍存在时不应删除 pid 文件")
			}
			removed = true
			return nil
		},
		sleep: func(time.Duration) {},
	})

	if err != nil {
		t.Fatalf("stopAllWeclawWithOps error: %v", err)
	}
	if !removed {
		t.Fatal("进程退出后应删除 pid 文件")
	}
	if len(signals) != 1 || signals[0] != syscall.SIGTERM {
		t.Fatalf("signals=%v, want only SIGTERM", signals)
	}
}

func TestStopAllWeclawKillsProcessGroupAfterGracefulTimeout(t *testing.T) {
	existsChecks := 0
	removed := false
	var pidSignals []syscall.Signal
	var groupSignals []syscall.Signal

	err := stopAllWeclawWithOps(stopProcessOps{
		readPid: func() (int, error) { return 1234, nil },
		processExists: func(int) bool {
			existsChecks++
			return existsChecks <= gracefulStopChecks+1
		},
		signalPID: func(_ int, sig syscall.Signal) error {
			pidSignals = append(pidSignals, sig)
			return nil
		},
		signalProcessGroup: func(_ int, sig syscall.Signal) error {
			groupSignals = append(groupSignals, sig)
			return nil
		},
		removePIDFile: func() error {
			removed = true
			return nil
		},
		sleep: func(time.Duration) {},
	})

	if err != nil {
		t.Fatalf("stopAllWeclawWithOps error: %v", err)
	}
	if !removed {
		t.Fatal("强杀后确认退出时应删除 pid 文件")
	}
	if len(pidSignals) != 2 || pidSignals[0] != syscall.SIGTERM || pidSignals[1] != syscall.SIGKILL {
		t.Fatalf("pidSignals=%v, want SIGTERM then SIGKILL", pidSignals)
	}
	if len(groupSignals) != 1 || groupSignals[0] != syscall.SIGKILL {
		t.Fatalf("groupSignals=%v, want SIGKILL", groupSignals)
	}
}

func TestStopAllWeclawKeepsPidFileWhenProcessSurvivesKill(t *testing.T) {
	err := stopAllWeclawWithOps(stopProcessOps{
		readPid:            func() (int, error) { return 1234, nil },
		processExists:      func(int) bool { return true },
		signalPID:          func(int, syscall.Signal) error { return nil },
		signalProcessGroup: func(int, syscall.Signal) error { return nil },
		removePIDFile: func() error {
			t.Fatal("进程仍存在时不应删除 pid 文件")
			return nil
		},
		sleep: func(time.Duration) {},
	})

	if err == nil {
		t.Fatal("stopAllWeclawWithOps error = nil, want process survival error")
	}
}

func TestStopAllWeclawRemovesStalePidFile(t *testing.T) {
	removed := false
	err := stopAllWeclawWithOps(stopProcessOps{
		readPid:            func() (int, error) { return 1234, nil },
		processExists:      func(int) bool { return false },
		signalPID:          func(int, syscall.Signal) error { return errors.New("不应发送信号") },
		signalProcessGroup: func(int, syscall.Signal) error { return errors.New("不应发送信号") },
		removePIDFile: func() error {
			removed = true
			return nil
		},
		sleep: func(time.Duration) {},
	})

	if err != nil {
		t.Fatalf("stopAllWeclawWithOps error: %v", err)
	}
	if !removed {
		t.Fatal("陈旧 pid 文件应被删除")
	}
}

func TestProcessExistsTreatsPermissionDeniedAsRunning(t *testing.T) {
	if !processSignalMeansExists(syscall.EPERM) {
		t.Fatal("signal 0 返回 EPERM 时说明进程存在，不应视为过期 pid")
	}
}

func TestRemovePIDFileTreatsMissingFileAsClean(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())

	if err := removePIDFile(); err != nil {
		t.Fatalf("removePIDFile error=%v，缺失 pid 文件应视为已清理", err)
	}
}

func TestStopAllWeclawDoesNotSignalWhenRuntimeLockIsFree(t *testing.T) {
	removed := false
	signaled := false
	err := stopAllWeclawWithOps(stopProcessOps{
		readPid:         func() (int, error) { return 1234, nil },
		processExists:   func(int) bool { return true },
		runtimeLockBusy: func() bool { return false },
		signalPID:       func(int, syscall.Signal) error { signaled = true; return nil },
		signalProcessGroup: func(int, syscall.Signal) error {
			signaled = true
			return nil
		},
		removePIDFile: func() error {
			removed = true
			return nil
		},
		sleep: func(time.Duration) {},
	})

	if err != nil {
		t.Fatalf("stopAllWeclawWithOps error: %v", err)
	}
	if signaled {
		t.Fatal("runtime lock 已释放时不应向 pid 文件里的进程发信号")
	}
	if !removed {
		t.Fatal("runtime lock 已释放时应清理陈旧 pid 文件")
	}
}

func TestReadRuntimeStateSupportsLegacyPidFile(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	path := mustResolveWeclawFile(t, "weclaw.pid")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create weclaw dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
		t.Fatalf("write legacy pid: %v", err)
	}

	state, err := readRuntimeState()

	if err != nil {
		t.Fatalf("readRuntimeState error: %v", err)
	}
	if state.PID != 1234 {
		t.Fatalf("PID=%d, want 1234", state.PID)
	}
}

func TestWriteRuntimeStatePersistsExecutableIdentity(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())

	err := writeRuntimeState(runtimeState{
		PID:       1234,
		Exe:       "/tmp/weclaw",
		Version:   "test-version",
		Mode:      "foreground",
		StartedAt: time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("writeRuntimeState error: %v", err)
	}

	state, err := readRuntimeState()
	if err != nil {
		t.Fatalf("readRuntimeState error: %v", err)
	}
	if state.PID != 1234 || state.Exe != "/tmp/weclaw" || state.Mode != "foreground" {
		t.Fatalf("state=%+v, want persisted pid/exe/mode", state)
	}
	info, err := os.Stat(mustResolveWeclawFile(t, "weclaw.pid"))
	if err != nil {
		t.Fatalf("stat runtime state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("runtime state mode=%#o, want 0600", got)
	}
}

func TestCurrentServiceModeDetectsSystemdInvocation(t *testing.T) {
	t.Setenv(daemonChildEnv, "")
	t.Setenv("INVOCATION_ID", "test-systemd-invocation")
	if got := currentServiceMode(); got != "systemd" {
		t.Fatalf("currentServiceMode=%q, want systemd", got)
	}
}

func TestWriteRuntimeStateRejectsSymlinkAndPreservesTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	target := filepath.Join(home, "runtime-target")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, mustResolveWeclawFile(t, "weclaw.pid")); err != nil {
		t.Fatalf("create runtime state symlink: %v", err)
	}

	err := writeRuntimeState(runtimeState{PID: 1234})
	if err == nil {
		t.Error("writeRuntimeState error = nil, want symlink rejection")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if got := string(data); got != "original" {
		t.Fatalf("target=%q, want original", got)
	}
}

func TestReadRuntimeStateRejectsSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	target := filepath.Join(home, "runtime-target")
	if err := os.WriteFile(target, []byte("1234"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, mustResolveWeclawFile(t, "weclaw.pid")); err != nil {
		t.Fatalf("create runtime state symlink: %v", err)
	}

	if _, err := readRuntimeState(); err == nil {
		t.Fatal("readRuntimeState error = nil, want symlink rejection")
	}
}

func TestAcquireRuntimeLockRejectsSymlinkAndPreservesTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	target := filepath.Join(home, "lock-target")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, mustResolveWeclawFile(t, "weclaw.lock")); err != nil {
		t.Fatalf("create runtime lock symlink: %v", err)
	}

	lock, err := acquireRuntimeLock()
	if err == nil {
		if lock != nil {
			_ = lock.Close()
		}
		t.Error("acquireRuntimeLock error = nil, want symlink rejection")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if got := string(data); got != "original" {
		t.Fatalf("target=%q, want original", got)
	}
}

func TestRuntimeStateWriteFailsClosedWhenDataDirUnavailable(t *testing.T) {
	t.Setenv("WECLAW_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := config.DataDir(); err == nil {
		t.Skip("platform still resolves a home directory without HOME or USERPROFILE")
	}
	workingDir := t.TempDir()
	if err := os.Chmod(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workingDir)
	decoy := filepath.Join(workingDir, "weclaw.pid")
	if err := os.WriteFile(decoy, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeRuntimeState(runtimeState{PID: 1234}); err == nil {
		t.Error("writeRuntimeState error = nil, want data-dir resolution failure")
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatalf("read decoy: %v", err)
	}
	if got := string(data); got != "do-not-touch" {
		t.Fatalf("decoy=%q, want unchanged", got)
	}
	info, err := os.Stat(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("working directory mode=%#o, want 0755", got)
	}
}

func TestRuntimeLockFailsClosedWhenDataDirUnavailable(t *testing.T) {
	t.Setenv("WECLAW_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := config.DataDir(); err == nil {
		t.Skip("platform still resolves a home directory without HOME or USERPROFILE")
	}
	workingDir := t.TempDir()
	if err := os.Chmod(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workingDir)

	lock, err := acquireRuntimeLock()
	if err == nil {
		if lock != nil {
			_ = lock.Close()
		}
		t.Error("acquireRuntimeLock error = nil, want data-dir resolution failure")
	}
	if _, statErr := os.Lstat(filepath.Join(workingDir, "weclaw.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("runtime lock created in working directory: %v", statErr)
	}
	info, statErr := os.Stat(workingDir)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("working directory mode=%#o, want 0755", got)
	}
}

func TestRemoveRuntimeStateFailsClosedWhenDataDirUnavailable(t *testing.T) {
	t.Setenv("WECLAW_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := config.DataDir(); err == nil {
		t.Skip("platform still resolves a home directory without HOME or USERPROFILE")
	}
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	decoy := filepath.Join(workingDir, "weclaw.pid")
	if err := os.WriteFile(decoy, []byte("do-not-remove"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeRuntimeState(); err == nil {
		t.Error("removeRuntimeState error = nil, want data-dir resolution failure")
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatalf("read decoy: %v", err)
	}
	if got := string(data); got != "do-not-remove" {
		t.Fatalf("decoy=%q, want unchanged", got)
	}
}

func TestFeishuDedupStateFileFailsClosedWhenDataDirUnavailable(t *testing.T) {
	t.Setenv("WECLAW_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := config.DataDir(); err == nil {
		t.Skip("platform still resolves a home directory without HOME or USERPROFILE")
	}

	if path, err := feishuDedupStateFile("cli_test"); err == nil || path != "" {
		t.Fatalf("feishuDedupStateFile=(%q, %v), want empty path and data-dir error", path, err)
	}
}

func TestAcquireRuntimeLockRejectsSecondHolder(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())

	first, err := acquireRuntimeLock()
	if err != nil {
		t.Fatalf("first acquireRuntimeLock error: %v", err)
	}
	defer first.Close()

	second, err := acquireRuntimeLock()
	if err == nil {
		_ = second.Close()
		t.Fatal("second acquireRuntimeLock error = nil, want already running")
	}
	if !strings.Contains(err.Error(), "weclaw 已在运行") {
		t.Fatalf("error=%v, want running hint", err)
	}
}

func mustResolveWeclawFile(t *testing.T, name string) string {
	t.Helper()
	path, err := resolveWeclawFile(name)
	if err != nil {
		t.Fatalf("resolve WeClaw file %q: %v", name, err)
	}
	return path
}

func TestAcquireDaemonLaunchLockRejectsSecondLauncher(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())

	first, err := acquireDaemonLaunchLock()
	if err != nil {
		t.Fatalf("first acquireDaemonLaunchLock error: %v", err)
	}
	defer first.Close()

	second, err := acquireDaemonLaunchLock()
	if err == nil {
		_ = second.Close()
		t.Fatal("second acquireDaemonLaunchLock error = nil, want already starting")
	}
	if !strings.Contains(err.Error(), "weclaw 正在启动") {
		t.Fatalf("error=%v, want starting hint", err)
	}
}

func TestHandleDaemonPIDWriteFailureStopsStartedProcess(t *testing.T) {
	stopped := false
	released := false
	err := handleDaemonPIDWriteResult(errors.New("write failed"), daemonPIDWriteProcess{
		kill: func() error {
			stopped = true
			return nil
		},
		wait: func() error {
			return nil
		},
		release: func() error {
			released = true
			return nil
		},
	})

	if err == nil {
		t.Fatal("handleDaemonPIDWriteResult error = nil, want write failure")
	}
	if !stopped {
		t.Fatal("pid 写入失败时应停止已启动进程")
	}
	if released {
		t.Fatal("pid 写入失败时不应 release 失控进程")
	}
}
