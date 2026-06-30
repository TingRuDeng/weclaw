//go:build windows

package agent

import "os/exec"

// configureProcessGroup 在 windows 上退化为无操作。
func configureProcessGroup(cmd *exec.Cmd) {}

// gracefulCancel 在 windows 上直接结束进程（无进程组语义）。
func gracefulCancel(cmd *exec.Cmd) func() error {
	return func() error {
		if cmd == nil || cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}

// sweepProcessGroup 在 windows 上退化为结束主进程。
func sweepProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
