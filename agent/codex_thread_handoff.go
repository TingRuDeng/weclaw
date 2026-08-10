package agent

import (
	"context"
	"fmt"
	"strings"
)

// RecoverCodexThreadHandoff restarts the verified official daemon only when
// Codex App is present and the entire shared Host is provably idle. Restarting
// drops upstream loaded-thread writer locks; existing conversation mappings
// remain durable and resume lazily on their next use.
func (a *ACPAgent) RecoverCodexThreadHandoff(ctx context.Context, previousThreadID string) (attempted bool, err error) {
	previousThreadID = strings.TrimSpace(previousThreadID)
	if previousThreadID == "" || a.protocol != protocolCodexAppServer ||
		!a.codexDesktopCoordination || !a.usesOfficialCodexDaemon() || a.desktopProbe == nil {
		return false, nil
	}
	desktopSocketExists, desktopProcessExists := a.desktopProbe.Presence()
	if !desktopProcessExists {
		return false, nil
	}
	if !desktopSocketExists {
		return true, ErrCodexDesktopUnavailable
	}
	switch a.codexRuntimeModeSnapshot() {
	case CodexRuntimeDesktop:
		return false, nil
	case CodexRuntimeWeClaw:
		attempted = true
	default:
		return true, ErrCodexRuntimeUnavailable
	}

	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	gate := a.ensureCodexAppServerGate()
	if err := gate.beginExclusive(); err != nil {
		return true, err
	}
	committed, available := false, true
	defer func() { gate.finishExclusive(committed, available) }()

	if err := a.ensureCodexThreadHandoffIdle(ctx); err != nil {
		return true, err
	}
	if a.codexRuntimeModeSnapshot() != CodexRuntimeWeClaw {
		return true, ErrCodexRuntimeUnavailable
	}
	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return true, err
	}
	if err := a.prepareCodexHostSocket(socketPath); err != nil {
		return true, err
	}
	lifecycleLock, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return true, err
	}
	defer releaseCodexHostStartupLock(lifecycleLock)

	if err := a.ensureCodexThreadHandoffIdle(ctx); err != nil {
		return true, err
	}
	if a.codexRuntimeModeSnapshot() != CodexRuntimeWeClaw {
		return true, ErrCodexRuntimeUnavailable
	}

	available = false
	if err := a.stopManagedHost(ctx, socketPath); err != nil {
		return true, fmt.Errorf("停止共享 Codex Host 以回交旧会话: %w", err)
	}
	restartCtx, cancelRestart := context.WithTimeout(context.WithoutCancel(ctx), acpStartupTimeout)
	defer cancelRestart()
	if err := a.startManagedHost(restartCtx, socketPath); err != nil {
		return true, fmt.Errorf("重启共享 Codex Host 以回交旧会话: %w", err)
	}
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)
	if a.codexOwners != nil {
		a.codexOwners.invalidateRuntimeAuthority(CodexRuntimeWeClaw)
	}
	a.markAllCodexThreadsResumeOnFirstUse()
	available = true
	committed = true
	return true, nil
}

func (a *ACPAgent) ensureCodexThreadHandoffIdle(ctx context.Context) error {
	if a.codexOwners != nil {
		if count, uncertain := a.codexOwners.anyWriterLeaseStatus(); count > 0 {
			if uncertain {
				return fmt.Errorf("%w: 存在终态未确认的写入任务", ErrCodexWriterBusy)
			}
			return ErrCodexWriterBusy
		}
		if active, unknown := a.codexOwners.anyActiveThreadStatus(); active > 0 || unknown {
			return fmt.Errorf("%w: 存在活动或运行态未知的 thread", ErrCodexWriterBusy)
		}
	}
	if err := a.ensureAllCodexThreadsIdle(ctx); err != nil {
		return fmt.Errorf("%w: 无法确认共享 Host 全局空闲: %v", ErrCodexWriterBusy, err)
	}
	return nil
}
