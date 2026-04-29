package cmd

import (
	"errors"
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
