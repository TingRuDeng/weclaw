//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup 让子进程独立成组，便于一次性回收其派生的 bash 等子进程树。
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalProcessGroup 向整个进程组发送信号（负 PID 表示进程组）。
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}

// gracefulCancel 在 ctx 取消时先对进程组发送 SIGINT，做优雅中止。
func gracefulCancel(cmd *exec.Cmd) func() error {
	return func() error {
		return signalProcessGroup(cmd, syscall.SIGINT)
	}
}

// sweepProcessGroup 兜底强杀残留子进程，避免卡死命令留下孤儿进程。
func sweepProcessGroup(cmd *exec.Cmd) {
	_ = signalProcessGroup(cmd, syscall.SIGKILL)
}
