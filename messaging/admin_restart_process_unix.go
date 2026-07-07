//go:build !windows

package messaging

import (
	"os/exec"
	"syscall"
)

// configureDetachedRestartCommand 让远程 restart 子进程脱离当前服务进程组，避免停止旧服务时被级联强杀。
func configureDetachedRestartCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
