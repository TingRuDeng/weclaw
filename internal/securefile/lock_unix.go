//go:build unix

package securefile

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type exclusiveLock struct {
	file *os.File
}

func acquireExclusiveLock(ctx context.Context, path string) (*exclusiveLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("acquire secure lock: %w", err)
	}
	file, err := OpenAppend(path)
	if err != nil {
		return nil, fmt.Errorf("open secure lock: %w", err)
	}

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire secure lock: %w", err)
		}
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &exclusiveLock{file: file}, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			_ = file.Close()
			return nil, fmt.Errorf("acquire secure lock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("acquire secure lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (l *exclusiveLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	_ = l.file.Close()
}
