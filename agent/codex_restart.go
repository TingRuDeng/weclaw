package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrCodexDesktopFrontendActive = errors.New("Codex App 仍在运行")
	ErrCodexRestartUnsafe         = errors.New("Codex Host 重启状态无法确认")
)

// CodexRestartSnapshot is the minimum non-secret Host identity persisted
// across a WeClaw process replacement.
type CodexRestartSnapshot struct {
	HostMode       string `json:"host_mode"`
	SocketPath     string `json:"socket_path"`
	HostGeneration uint64 `json:"host_generation"`
	HostStopped    bool   `json:"host_stopped"`
}

// CodexRestartController coordinates one native Codex shared Host with the
// surrounding WeClaw restart transaction.
type CodexRestartController interface {
	PrepareCodexRestart(context.Context, func(CodexRestartSnapshot) error) (CodexRestartSnapshot, error)
	CancelCodexRestart(context.Context) error
	VerifyCodexRestart(context.Context, CodexRestartSnapshot) (CodexRestartSnapshot, error)
}

// CodexDesktopFrontendPresence exposes only the conservative local presence
// bit needed by an offline `weclaw restart`; it does not grant process control.
func CodexDesktopFrontendPresence() (socketExists bool, processExists bool) {
	return codexDesktopPresence()
}

// PrepareCodexRestart proves global idleness and stops the verified shared
// Host. The app-server gate intentionally remains failed-closed until either
// the service exits or CancelCodexRestart reconstructs the Host.
func (a *ACPAgent) PrepareCodexRestart(
	ctx context.Context,
	persistIntent func(CodexRestartSnapshot) error,
) (CodexRestartSnapshot, error) {
	if a == nil || !a.usesCodexSharedHost() {
		return CodexRestartSnapshot{}, fmt.Errorf("当前 Agent 不是 Codex shared app-server")
	}
	if persistIntent == nil {
		return CodexRestartSnapshot{}, fmt.Errorf("缺少 Codex Host 重启意图持久化回调")
	}
	a.codexRestartMu.Lock()
	if a.codexRestartPrepared {
		snapshot := a.codexRestartSnapshot
		a.codexRestartMu.Unlock()
		if err := persistIntent(snapshot); err != nil {
			return CodexRestartSnapshot{}, fmt.Errorf("持久化 Codex Host 重启意图: %w", err)
		}
		return snapshot, nil
	}
	a.codexRestartMu.Unlock()

	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	a.codexRestartMu.Lock()
	if a.codexRestartPrepared {
		snapshot := a.codexRestartSnapshot
		a.codexRestartMu.Unlock()
		if err := persistIntent(snapshot); err != nil {
			return CodexRestartSnapshot{}, fmt.Errorf("持久化 Codex Host 重启意图: %w", err)
		}
		return snapshot, nil
	}
	a.codexRestartMu.Unlock()

	gate := a.ensureCodexAppServerGate()
	if err := gate.beginExclusive(); err != nil {
		return CodexRestartSnapshot{}, fmt.Errorf("Codex Host 正在执行任务或维护操作: %w", err)
	}
	available := true
	committed := false
	defer func() {
		if !committed {
			gate.finishExclusive(false, available)
		}
	}()

	if err := a.requireCodexDesktopAbsent(); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if err := a.requireCodexRestartIdle(ctx); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if err := a.ensureStarted(ctx); err != nil {
		return CodexRestartSnapshot{}, fmt.Errorf("启动并验证 Codex Host: %w", err)
	}
	if a.codexRuntimeModeSnapshot() != CodexRuntimeWeClaw {
		return CodexRestartSnapshot{}, fmt.Errorf("%w: 当前 Host 权威不是 WeClaw", ErrCodexRuntimeUnavailable)
	}
	if err := a.requireCodexRestartIdle(ctx); err != nil {
		return CodexRestartSnapshot{}, err
	}

	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	if err := a.prepareCodexHostSocket(socketPath); err != nil {
		return CodexRestartSnapshot{}, err
	}
	lifecycleLock, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	defer releaseCodexHostStartupLock(lifecycleLock)
	if err := a.requireCodexDesktopAbsent(); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if err := a.requireCodexRestartIdle(ctx); err != nil {
		return CodexRestartSnapshot{}, err
	}
	metadata, err := a.validateManagedCodexHost(socketPath)
	if err != nil {
		return CodexRestartSnapshot{}, fmt.Errorf("验证待停止的 Codex Host: %w", err)
	}
	if err := a.preflightCodexHostConflicts(ctx, metadata.PID); err != nil {
		return CodexRestartSnapshot{}, err
	}
	snapshot := CodexRestartSnapshot{
		HostMode: strings.TrimSpace(a.codexHostMode), SocketPath: socketPath,
		HostGeneration: metadata.Generation, HostStopped: true,
	}
	// Persist the old generation before process mutation. If either process
	// crashes after this point, startup recovery can still prove replacement.
	if err := persistIntent(snapshot); err != nil {
		return CodexRestartSnapshot{}, fmt.Errorf("持久化 Codex Host 重启意图: %w", err)
	}
	if err := a.requireCodexDesktopAbsent(); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if err := a.requireCodexRestartIdle(ctx); err != nil {
		return CodexRestartSnapshot{}, err
	}
	a.codexRestartMu.Lock()
	a.codexRestartSnapshot = snapshot
	a.codexRestartPrepared = true
	a.codexRestartMu.Unlock()

	available = false
	if err := a.stopManagedHost(ctx, socketPath); err != nil {
		return CodexRestartSnapshot{}, fmt.Errorf("%w: 停止 Codex Host: %v", ErrCodexRestartUnsafe, err)
	}
	a.setCodexRuntimeMode(CodexRuntimeUnknown)
	if a.codexOwners != nil {
		a.codexOwners.invalidateRuntimeAuthority(CodexRuntimeWeClaw)
	}
	gate.finishExclusive(true, false)
	committed = true
	return snapshot, nil
}

// CancelCodexRestart reconstructs a Host stopped by PrepareCodexRestart before
// reopening the app-server gate. It is used when the outer service restart
// fails before the old process exits.
func (a *ACPAgent) CancelCodexRestart(ctx context.Context) error {
	if a == nil || !a.usesCodexSharedHost() {
		return nil
	}
	a.codexRestartMu.Lock()
	prepared := a.codexRestartPrepared
	previous := a.codexRestartSnapshot
	a.codexRestartMu.Unlock()
	if !prepared {
		return nil
	}
	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	if err := a.requireCodexDesktopAbsent(); err != nil {
		return err
	}
	verified, err := a.verifyStartedCodexHost(ctx, previous)
	if err != nil {
		return fmt.Errorf("恢复重启前的 Codex Host: %w", err)
	}
	a.ensureCodexAppServerGate().finishRestart(true)
	a.codexRestartMu.Lock()
	a.codexRestartSnapshot = verified
	a.codexRestartPrepared = false
	a.codexRestartMu.Unlock()
	return nil
}

// VerifyCodexRestart starts or attaches the one configured shared Host and
// proves that a stopped Host was replaced by a new generation.
func (a *ACPAgent) VerifyCodexRestart(ctx context.Context, previous CodexRestartSnapshot) (CodexRestartSnapshot, error) {
	if a == nil || !a.usesCodexSharedHost() {
		return CodexRestartSnapshot{}, fmt.Errorf("当前 Agent 不是 Codex shared app-server")
	}
	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	if err := a.requireCodexDesktopAbsent(); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if previous.HostStopped {
		currentMode := strings.TrimSpace(a.codexHostMode)
		if previous.HostMode != "" && currentMode != previous.HostMode {
			return CodexRestartSnapshot{}, fmt.Errorf(
				"%w: Codex Host mode 已从 %s 变为 %s",
				ErrCodexRuntimeUnavailable, previous.HostMode, currentMode,
			)
		}
		currentSocket, err := a.resolveCodexHostSocket()
		if err != nil {
			return CodexRestartSnapshot{}, err
		}
		if previous.SocketPath != "" && filepath.Clean(currentSocket) != filepath.Clean(previous.SocketPath) {
			return CodexRestartSnapshot{}, fmt.Errorf("%w: Codex Host socket 已变更", ErrCodexRuntimeUnavailable)
		}
	}
	return a.verifyStartedCodexHost(ctx, previous)
}

func (a *ACPAgent) verifyStartedCodexHost(ctx context.Context, previous CodexRestartSnapshot) (CodexRestartSnapshot, error) {
	current, err := a.inspectStartedCodexHost(ctx)
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	if !previous.HostStopped || previous.HostGeneration == 0 || current.HostGeneration != previous.HostGeneration {
		return current, nil
	}
	if err := a.replaceStaleCodexHostGeneration(ctx, previous); err != nil {
		return CodexRestartSnapshot{}, err
	}
	current, err = a.inspectStartedCodexHost(ctx)
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	if current.HostGeneration == previous.HostGeneration {
		return CodexRestartSnapshot{}, fmt.Errorf("%w: Codex Host generation 未变化", ErrCodexRuntimeUnavailable)
	}
	return current, nil
}

func (a *ACPAgent) inspectStartedCodexHost(ctx context.Context) (CodexRestartSnapshot, error) {
	if err := a.ensureStarted(ctx); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if err := a.requireCodexDesktopAbsent(); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if a.codexRuntimeModeSnapshot() != CodexRuntimeWeClaw {
		return CodexRestartSnapshot{}, fmt.Errorf("%w: 重启后 Host 权威不是 WeClaw", ErrCodexRuntimeUnavailable)
	}
	if err := a.requireCodexRestartIdle(ctx); err != nil {
		return CodexRestartSnapshot{}, err
	}
	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	metadata, err := a.validateManagedCodexHost(socketPath)
	if err != nil {
		return CodexRestartSnapshot{}, fmt.Errorf("验证重启后的 Codex Host: %w", err)
	}
	if err := a.preflightCodexHostConflicts(ctx, metadata.PID); err != nil {
		return CodexRestartSnapshot{}, err
	}
	return CodexRestartSnapshot{
		HostMode: strings.TrimSpace(a.codexHostMode), SocketPath: socketPath,
		HostGeneration: metadata.Generation, HostStopped: false,
	}, nil
}

func (a *ACPAgent) replaceStaleCodexHostGeneration(ctx context.Context, previous CodexRestartSnapshot) error {
	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return err
	}
	if err := a.prepareCodexHostSocket(socketPath); err != nil {
		return err
	}
	lifecycleLock, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return err
	}
	if err := a.requireCodexDesktopAbsent(); err != nil {
		releaseCodexHostStartupLock(lifecycleLock)
		return err
	}
	if err := a.requireCodexRestartIdle(ctx); err != nil {
		releaseCodexHostStartupLock(lifecycleLock)
		return err
	}
	metadata, err := a.validateManagedCodexHost(socketPath)
	if err != nil {
		releaseCodexHostStartupLock(lifecycleLock)
		return fmt.Errorf("验证待替换的 Codex Host: %w", err)
	}
	if metadata.Generation != previous.HostGeneration {
		releaseCodexHostStartupLock(lifecycleLock)
		return nil
	}
	if err := a.preflightCodexHostConflicts(ctx, metadata.PID); err != nil {
		releaseCodexHostStartupLock(lifecycleLock)
		return err
	}
	if err := a.stopManagedHost(ctx, socketPath); err != nil {
		releaseCodexHostStartupLock(lifecycleLock)
		return fmt.Errorf("%w: 停止旧一代 Codex Host: %v", ErrCodexRestartUnsafe, err)
	}
	releaseCodexHostStartupLock(lifecycleLock)
	// rpcCall is the existing in-process test seam; real Host startup always
	// re-establishes runtime authority itself.
	if a.rpcCall == nil {
		a.setCodexRuntimeMode(CodexRuntimeUnknown)
		if a.codexOwners != nil {
			a.codexOwners.invalidateRuntimeAuthority(CodexRuntimeWeClaw)
		}
	}
	if err := a.ensureStarted(ctx); err != nil {
		return fmt.Errorf("启动新一代 Codex Host: %w", err)
	}
	return nil
}

func (a *ACPAgent) requireCodexDesktopAbsent() error {
	var socketExists, processExists bool
	switch {
	case a.desktopProbe != nil:
		socketExists, processExists = a.desktopProbe.Presence()
	case a.codexDesktopPresenceCall != nil:
		socketExists, processExists = a.codexDesktopPresenceCall()
	default:
		socketExists, processExists = codexDesktopPresence()
	}
	if socketExists || processExists {
		return fmt.Errorf("%w；请完整退出 Codex App 后重试", ErrCodexDesktopFrontendActive)
	}
	return nil
}

func (a *ACPAgent) requireCodexRestartIdle(ctx context.Context) error {
	if a.codexOwners != nil {
		if count, uncertain := a.codexOwners.anyWriterLeaseStatus(); count > 0 {
			if uncertain {
				return fmt.Errorf("%w: 存在 %d 个终态未确认的 writer lease", ErrCodexWriterBusy, count)
			}
			return fmt.Errorf("%w: 存在 %d 个 writer lease", ErrCodexWriterBusy, count)
		}
		if active, unknown := a.codexOwners.anyActiveThreadStatus(); active > 0 || unknown {
			return fmt.Errorf("%w: 存在 %d 个活动 thread，unknown=%t", ErrCodexWriterBusy, active, unknown)
		}
	}
	if a.isRuntimeStarted() {
		idleCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := a.ensureAllCodexThreadsIdle(idleCtx); err != nil {
			return fmt.Errorf("%w: 无法确认所有 Codex thread 均为空闲: %v", ErrCodexWriterBusy, err)
		}
	}
	return nil
}
