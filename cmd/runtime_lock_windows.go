//go:build windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

type runtimeLock struct {
	path string
	file *os.File
}

// acquireExclusiveLockFile 用独占创建语义实现可复用的非阻塞锁。
func acquireExclusiveLockFile(path string, busyError func() string) (*runtimeLock, error) {
	if err := securefile.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%s", busyError())
		}
		return nil, err
	}
	return &runtimeLock{path: path, file: file}, nil
}

// acquireRuntimeLock 在 Windows 上用独占创建退化实现单实例保护。
func acquireRuntimeLock() (*runtimeLock, error) {
	path, err := resolveWeclawFile("weclaw.lock")
	if err != nil {
		return nil, err
	}
	return acquireExclusiveLockFile(path, func() string {
		return "weclaw 已在运行" + runtimeLockHolderHint()
	})
}

// acquireDaemonLaunchLock 串行化后台启动父进程，避免锁交接窗口内互相 stop/start。
func acquireDaemonLaunchLock() (*runtimeLock, error) {
	path, err := resolveWeclawFile("weclaw.start.lock")
	if err != nil {
		return nil, err
	}
	return acquireExclusiveLockFile(path, func() string {
		return "weclaw 正在启动，请稍后重试"
	})
}

// Close 释放退化锁文件，Windows 下用删除文件表示释放。
func (l *runtimeLock) Close() error {
	if l == nil {
		return nil
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	if l.path != "" {
		return os.Remove(l.path)
	}
	return nil
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
