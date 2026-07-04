//go:build windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

type runtimeLock struct {
	path string
	file *os.File
}

// runtimeLockFile 返回单实例锁文件路径，锁文件本身可长期保留。
func runtimeLockFile() string {
	return filepath.Join(weclawDir(), "weclaw.lock")
}

// acquireRuntimeLock 在 Windows 上用独占创建退化实现单实例保护。
func acquireRuntimeLock() (*runtimeLock, error) {
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		return nil, err
	}
	path := runtimeLockFile()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("weclaw 已在运行%s", runtimeLockHolderHint())
		}
		return nil, err
	}
	return &runtimeLock{path: path, file: file}, nil
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
