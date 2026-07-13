package agent

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// assertProcessExited 使用 signal 0 等待指定测试子进程彻底退出。
func assertProcessExited(t *testing.T, pid int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Fatal("Windows 尚无可靠的 signal 0 进程退出探测")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find process %d: %v", pid, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		signalErr := process.Signal(syscall.Signal(0))
		if errors.Is(signalErr, os.ErrProcessDone) || errors.Is(signalErr, syscall.ESRCH) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("helper process pid=%d still exists", pid)
}
