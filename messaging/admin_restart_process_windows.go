//go:build windows

package messaging

import "os/exec"

// configureDetachedRestartCommand 在 Windows 下保持默认进程模型。
func configureDetachedRestartCommand(_ *exec.Cmd) {}
