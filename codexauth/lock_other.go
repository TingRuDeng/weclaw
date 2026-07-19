//go:build !unix

package codexauth

import (
	"context"
	"fmt"
	"os"
	"time"
)

type fileLock struct {
	file *os.File
	path string
}

func acquireFileLock(ctx context.Context, path string) (*fileLock, error) {
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			return &fileLock{file: file, path: path}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("open account lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, NewError(CodeBusy, "Codex 账号切换正由其他进程处理", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (l *fileLock) release() {
	if l == nil {
		return
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	_ = os.Remove(l.path)
}
