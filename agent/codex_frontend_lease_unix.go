//go:build unix

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

var (
	ErrCodexRestartInProgress = errors.New("Codex Host 正在重启")
	ErrCodexCLIFrontendActive = errors.New("仍有受控 Codex CLI 正在运行")
)

// CodexFrontendLease serializes controlled CLI frontends with a coordinated
// Host restart. Kernel ownership releases the lease even when either process
// exits unexpectedly.
type CodexFrontendLease struct {
	file *os.File
}

// AcquireCodexCLIFrontendLease holds a shared lease for the entire interactive
// CLI lifetime. A coordinated restart therefore cannot pass its preflight
// while any controlled CLI is still attached.
func AcquireCodexCLIFrontendLease() (*CodexFrontendLease, error) {
	lease, err := acquireCodexFrontendLease(syscall.LOCK_SH, ErrCodexRestartInProgress)
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

// AcquireCodexRestartLease holds the exclusive restart fence from preflight
// until the replacement service has completed startup verification.
func AcquireCodexRestartLease() (*CodexFrontendLease, error) {
	return acquireCodexFrontendLease(syscall.LOCK_EX, ErrCodexCLIFrontendActive)
}

func acquireCodexFrontendLease(mode int, busy error) (*CodexFrontendLease, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, fmt.Errorf("解析 WeClaw 状态目录: %w", err)
	}
	path := filepath.Join(dataDir, "state", "codex-frontends.lock")
	file, err := securefile.OpenAppend(path)
	if err != nil {
		return nil, fmt.Errorf("打开 Codex frontend 租约: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, busy
		}
		return nil, fmt.Errorf("锁定 Codex frontend 租约: %w", err)
	}
	return &CodexFrontendLease{file: file}, nil
}

func (lease *CodexFrontendLease) Close() error {
	if lease == nil || lease.file == nil {
		return nil
	}
	file := lease.file
	lease.file = nil
	return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
}
