//go:build !windows

package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type runtimeLock struct {
	file *os.File
}

// runtimeLockFile 返回单实例锁文件路径，锁文件本身可长期保留。
func runtimeLockFile() string {
	return filepath.Join(weclawDir(), "weclaw.lock")
}

// acquireLockFile 对指定锁文件加非阻塞排他锁，供运行锁和启动锁复用。
func acquireLockFile(path string, busyError func() string) (*runtimeLock, error) {
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%s", busyError())
		}
		return nil, err
	}
	return &runtimeLock{file: file}, nil
}

// acquireRuntimeLock 使用系统文件锁保证同一用户目录下只有一个服务实例。
func acquireRuntimeLock() (*runtimeLock, error) {
	return acquireLockFile(runtimeLockFile(), func() string {
		return "weclaw 已在运行" + runtimeLockHolderHint()
	})
}

// acquireDaemonLaunchLock 串行化后台启动父进程，避免锁交接窗口内互相 stop/start。
func acquireDaemonLaunchLock() (*runtimeLock, error) {
	return acquireLockFile(daemonLaunchLockFile(), func() string {
		return "weclaw 正在启动，请稍后重试"
	})
}

// Close 释放系统文件锁，服务进程退出时必须调用。
func (l *runtimeLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

// runtimeLockHolderHint 从运行状态里补充占锁进程信息。
func runtimeLockHolderHint() string {
	state, err := readRuntimeState()
	if err != nil {
		return ""
	}
	if state.Exe != "" {
		return fmt.Sprintf("：pid=%d path=%s", state.PID, state.Exe)
	}
	return fmt.Sprintf("：pid=%d", state.PID)
}
