//go:build unix

package codexauth

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type fileLock struct{ file *os.File }

func acquireFileLock(ctx context.Context, path string) (*fileLock, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open account lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open account lock: invalid file descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		file.Close()
		return nil, fmt.Errorf("inspect account lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || int(stat.Uid) != os.Geteuid() {
		file.Close()
		return nil, fmt.Errorf("account lock must be a 0600 regular file owned by the current user")
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &fileLock{file: file}, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			file.Close()
			return nil, fmt.Errorf("lock account store: %w", err)
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, NewError(CodeBusy, "Codex 账号切换正由其他进程处理", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (l *fileLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	_ = l.file.Close()
}
