package cmd

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

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

func TestReadRuntimeStateSupportsLegacyPidFile(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		t.Fatalf("create weclaw dir: %v", err)
	}
	if err := os.WriteFile(pidFile(), []byte("1234"), 0o600); err != nil {
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
