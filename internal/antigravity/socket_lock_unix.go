//go:build unix

package antigravity

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

type socketLock struct {
	file *os.File
}

func acquireSocketLock(socketPath string) (*socketLock, error) {
	file, err := securefile.OpenAppend(socketPath + ".lock")
	if err != nil {
		return nil, fmt.Errorf("open antigravity lock file: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock antigravity sidecar: %w", err)
	}
	return &socketLock{file: file}, nil
}

func (l *socketLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
