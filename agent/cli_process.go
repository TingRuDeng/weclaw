package agent

import (
	"os/exec"
	"time"
)

// turnKillGrace 是 ctx 取消后等待子进程优雅退出的宽限期，超时则强杀整个进程组。
const turnKillGrace = 5 * time.Second

// configureTurnProcess 让单轮子进程独立成组，并在 ctx 取消时先 SIGINT 优雅中止、
// 宽限期后由运行时强杀，配合 sweepProcessGroup 回收派生的 bash 等子进程树。
func configureTurnProcess(cmd *exec.Cmd) {
	configureProcessGroup(cmd)
	cmd.Cancel = gracefulCancel(cmd)
	cmd.WaitDelay = turnKillGrace
}
