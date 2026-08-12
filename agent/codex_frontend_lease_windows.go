//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/internal/securefile"
	"golang.org/x/sys/windows"
)

var (
	ErrCodexRestartInProgress = errors.New("Codex Host 正在重启")
	ErrCodexCLIFrontendActive = errors.New("仍有受控 Codex CLI 正在运行")
)

// CodexFrontendLease serializes controlled CLI frontends with a coordinated
// Host restart. Kernel ownership releases the lease on process termination.
type CodexFrontendLease struct {
	file       *os.File
	overlapped windows.Overlapped
}

func AcquireCodexCLIFrontendLease() (*CodexFrontendLease, error) {
	lease, err := acquireCodexFrontendLease(windows.LOCKFILE_FAIL_IMMEDIATELY, ErrCodexRestartInProgress)
	if err != nil {
		return nil, err
	}
	present, journalErr := codexRestartJournalPresent()
	if journalErr != nil || present {
		_ = lease.Close()
		if journalErr != nil {
			return nil, fmt.Errorf("检查 Codex Host 重启事务: %w", journalErr)
		}
		return nil, ErrCodexRestartInProgress
	}
	return lease, nil
}

func AcquireCodexRestartLease() (*CodexFrontendLease, error) {
	return acquireCodexFrontendLease(
		windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK,
		ErrCodexCLIFrontendActive,
	)
}

func acquireCodexFrontendLease(flags uint32, busy error) (*CodexFrontendLease, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, fmt.Errorf("解析 WeClaw 状态目录: %w", err)
	}
	path := filepath.Join(dataDir, "state", "codex-frontends.lock")
	file, err := securefile.OpenAppend(path)
	if err != nil {
		return nil, fmt.Errorf("打开 Codex frontend 租约: %w", err)
	}
	lease := &CodexFrontendLease{file: file}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &lease.overlapped); err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, busy
		}
		return nil, fmt.Errorf("锁定 Codex frontend 租约: %w", err)
	}
	return lease, nil
}

func (lease *CodexFrontendLease) Close() error {
	if lease == nil || lease.file == nil {
		return nil
	}
	file := lease.file
	lease.file = nil
	return errors.Join(
		windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &lease.overlapped),
		file.Close(),
	)
}
