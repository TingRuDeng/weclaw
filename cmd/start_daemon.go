package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

func weclawDir() string {
	dir, _ := config.DataDir()
	return dir
}

func pidFile() string {
	return filepath.Join(weclawDir(), "weclaw.pid")
}

// daemonLaunchLockFile 返回后台启动父进程的短生命周期锁文件路径。
func daemonLaunchLockFile() string {
	return filepath.Join(weclawDir(), "weclaw.start.lock")
}

func logFile() string {
	return filepath.Join(weclawDir(), "weclaw.log")
}

const (
	gracefulStopChecks   = 20
	gracefulStopInterval = 500 * time.Millisecond
	daemonReadyChecks    = 50
	daemonReadyInterval  = 100 * time.Millisecond
)

// runDaemon 启动后台子进程；普通 start 只负责启动，不隐式停止既有服务。
func runDaemon() error {
	launchLock, err := acquireDaemonLaunchLock()
	if err != nil {
		return err
	}
	defer launchLock.Close()

	runtimeLock, err := acquireRuntimeLock()
	if err != nil {
		return err
	}
	if err := runtimeLock.Close(); err != nil {
		return err
	}
	if err := agent.CleanupCompanionEndpoints(); err != nil {
		return fmt.Errorf("cleanup companion endpoints: %w", err)
	}

	// 确保日志目录存在。
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		return fmt.Errorf("create weclaw dir: %w", err)
	}

	// 后台子进程 stdout/stderr 都写入统一日志文件。
	lf, err := os.OpenFile(logFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	// 复用当前二进制启动真正的前台服务进程。
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	cmd := exec.Command(exe, "start", "-f")
	cmd.Env = append(os.Environ(), daemonChildEnv+"=1")
	cmd.Stdout = lf
	cmd.Stderr = lf
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		lf.Close()
		return fmt.Errorf("start daemon: %w", err)
	}

	pid := cmd.Process.Pid
	if err := writeRuntimeState(runtimeState{
		PID:       pid,
		Exe:       exe,
		Version:   Version,
		Mode:      "background",
		StartedAt: time.Now(),
	}); err != nil {
		lf.Close()
		return handleDaemonPIDWriteResult(err, daemonPIDWriteProcess{
			kill:    cmd.Process.Kill,
			wait:    cmd.Wait,
			release: cmd.Process.Release,
		})
	}
	if err := waitDaemonChildReady(pid); err != nil {
		lf.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	// 启动确认后释放子进程，避免父进程阻塞。
	if err := cmd.Process.Release(); err != nil {
		lf.Close()
		return fmt.Errorf("release daemon process: %w", err)
	}
	lf.Close()

	fmt.Printf("weclaw started in background (pid=%d)\n", pid)
	fmt.Printf("Log: %s\n", logFile())
	fmt.Printf("Stop: weclaw stop\n")
	return nil
}

// waitDaemonChildReady 等待后台子进程真正持有 runtime lock，避免父进程提前返回造成并发 start 抢占。
func waitDaemonChildReady(pid int) error {
	for i := 0; i < daemonReadyChecks; i++ {
		if !processExists(pid) {
			return fmt.Errorf("weclaw 后台子进程 pid=%d 已退出，未完成启动", pid)
		}
		lock, err := acquireRuntimeLock()
		if err != nil {
			return nil
		}
		_ = lock.Close()
		time.Sleep(daemonReadyInterval)
	}
	return fmt.Errorf("weclaw 后台子进程 pid=%d 未在超时内完成启动", pid)
}

type daemonPIDWriteProcess struct {
	kill    func() error
	wait    func() error
	release func() error
}

// handleDaemonPIDWriteResult 在 pid 文件写入失败时回收刚启动的进程，避免后台服务失控。
func handleDaemonPIDWriteResult(writeErr error, proc daemonPIDWriteProcess) error {
	if writeErr == nil {
		if proc.release != nil {
			return proc.release()
		}
		return nil
	}
	if proc.kill != nil {
		_ = proc.kill()
	}
	if proc.wait != nil {
		_ = proc.wait()
	}
	return fmt.Errorf("write pid file: %w", writeErr)
}

func processExists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return processSignalMeansExists(p.Signal(syscall.Signal(0)))
}

func processSignalMeansExists(err error) bool {
	return err == nil || errors.Is(err, syscall.EPERM)
}

type stopProcessOps struct {
	readPid            func() (int, error)
	processExists      func(int) bool
	runtimeLockBusy    func() bool
	signalPID          func(int, syscall.Signal) error
	signalProcessGroup func(int, syscall.Signal) error
	removePIDFile      func() error
	sleep              func(time.Duration)
}

func stopAllWeclaw() error {
	return stopAllWeclawWithOps(defaultStopProcessOps())
}

func defaultStopProcessOps() stopProcessOps {
	return stopProcessOps{
		readPid:            readPid,
		processExists:      processExists,
		runtimeLockBusy:    runtimeLockBusy,
		signalPID:          signalPID,
		signalProcessGroup: signalProcessGroup,
		removePIDFile:      removePIDFile,
		sleep:              time.Sleep,
	}
}

// removePIDFile 清理运行态文件；服务进程可能已在退出 defer 中先删掉它。
func removePIDFile() error {
	err := os.Remove(pidFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// stopAllWeclawWithOps 只停止 pid 文件指向的目标，避免按命令行扫描误杀其他进程。
func stopAllWeclawWithOps(ops stopProcessOps) error {
	pid, err := ops.readPid()
	if err != nil {
		return nil
	}
	if !ops.processExists(pid) {
		return ops.removePIDFile()
	}
	if ops.runtimeLockBusy != nil && !ops.runtimeLockBusy() {
		return ops.removePIDFile()
	}
	_ = ops.signalPID(pid, syscall.SIGTERM)
	if waitProcessExit(pid, ops) {
		return ops.removePIDFile()
	}

	_ = ops.signalProcessGroup(pid, syscall.SIGKILL)
	_ = ops.signalPID(pid, syscall.SIGKILL)
	if waitProcessExit(pid, ops) {
		return ops.removePIDFile()
	}
	return fmt.Errorf("weclaw process pid=%d did not exit", pid)
}

// runtimeLockBusy 用运行锁确认 pid 文件是否仍指向真实 WeClaw 服务。
func runtimeLockBusy() bool {
	lock, err := acquireRuntimeLock()
	if err != nil {
		return true
	}
	_ = lock.Close()
	return false
}

func waitProcessExit(pid int, ops stopProcessOps) bool {
	for i := 0; i < gracefulStopChecks; i++ {
		ops.sleep(gracefulStopInterval)
		if !ops.processExists(pid) {
			return true
		}
	}
	return false
}

func signalPID(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}
