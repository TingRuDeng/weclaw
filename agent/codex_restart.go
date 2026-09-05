package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	ErrCodexDesktopFrontendActive = errors.New("Codex App 仍在运行")
	ErrCodexRestartUnsafe         = errors.New("Codex Host 重启状态无法确认")
)

// CodexRestartSnapshot is the minimum non-secret Host identity persisted
// across a WeClaw process replacement.
type CodexRestartSnapshot struct {
	HostMode         string                         `json:"host_mode"`
	SocketPath       string                         `json:"socket_path"`
	HostGeneration   uint64                         `json:"host_generation"`
	HostStopped      bool                           `json:"host_stopped"`
	ConflictingHosts []CodexRestartConflictSnapshot `json:"conflicting_hosts,omitempty"`
}

// CodexRestartConflictSnapshot is the bounded, non-secret part of a process
// stop plan persisted before an explicitly authorized conflicting Host stop.
// It deliberately excludes argv, sockets, and executable paths.
type CodexRestartConflictSnapshot struct {
	Kind    string `json:"kind"`
	PGID    int    `json:"pgid"`
	PIDs    []int  `json:"pids"`
	Stopped bool   `json:"stopped"`
}

// CodexRestartOptions contains only explicit operator authority for a single
// coordinated restart. It deliberately does not change task-draining policy.
type CodexRestartOptions struct {
	StopConflictingCodexHosts bool
	ForceTerminateCodex       bool
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
	return a.PrepareCodexRestartWithOptions(ctx, persistIntent, CodexRestartOptions{})
}

// PrepareCodexRestartWithOptions performs one coordinated restart under the
// explicit authority carried by the local restart request. The default option
// remains strictly read-only for external Codex Hosts.
func (a *ACPAgent) PrepareCodexRestartWithOptions(
	ctx context.Context,
	persistIntent func(CodexRestartSnapshot) error,
	opts CodexRestartOptions,
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
	beginExclusive := gate.beginExclusive
	if opts.ForceTerminateCodex {
		beginExclusive = gate.beginForcedExclusive
	}
	if err := beginExclusive(); err != nil {
		return CodexRestartSnapshot{}, fmt.Errorf("Codex Host 正在执行任务或维护操作: %w", err)
	}
	available := true
	committed := false
	defer func() {
		if !committed {
			gate.finishExclusive(false, available)
		}
	}()

	stopConflictingCodexHosts := opts.StopConflictingCodexHosts || opts.ForceTerminateCodex
	if opts.ForceTerminateCodex {
		if err := a.forceCloseCodexDesktopApp(ctx); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}
	if !stopConflictingCodexHosts {
		if err := a.requireCodexDesktopAbsent(); err != nil {
			return CodexRestartSnapshot{}, err
		}
		if err := a.reconnectExistingCodexHostForRestart(ctx); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}
	if !opts.ForceTerminateCodex {
		if err := a.requireCodexRestartIdle(ctx); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}

	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	if !opts.ForceTerminateCodex {
		if err := a.prepareCodexHostSocket(socketPath); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}

	var snapshot CodexRestartSnapshot
	var plannedConflicts []codexHostConflictTarget
	if stopConflictingCodexHosts {
		initialGeneration := uint64(0)
		if metadata, metadataErr := a.validateManagedCodexHost(socketPath); metadataErr == nil {
			initialGeneration = metadata.Generation
		}
		snapshot = CodexRestartSnapshot{
			HostMode: strings.TrimSpace(a.codexHostMode), SocketPath: socketPath,
			HostGeneration: initialGeneration, HostStopped: true,
		}
		var err error
		snapshot, plannedConflicts, err = a.stopExplicitCodexHostConflicts(
			ctx, snapshot, persistIntent, opts.ForceTerminateCodex,
		)
		if err != nil {
			return CodexRestartSnapshot{}, err
		}
		if !a.usesOfficialCodexDaemon() {
			if err := a.requireCodexDesktopAbsent(); err != nil {
				return CodexRestartSnapshot{}, err
			}
		}
	}
	if opts.ForceTerminateCodex {
		// Force includes the current authority Host, so there is no managed Host
		// left to validate or stop through possibly stale lifecycle metadata. Drop
		// the old client generation before returning the prepared transaction.
		if len(plannedConflicts) == 0 {
			if err := persistIntent(snapshot); err != nil {
				return CodexRestartSnapshot{}, fmt.Errorf("持久化 Codex Host 强制停止意图: %w", err)
			}
		}
		a.codexRestartMu.Lock()
		a.codexRestartSnapshot = snapshot
		a.codexRestartPrepared = true
		a.codexRestartMu.Unlock()
		a.detachForceStoppedCodexHostClient()
		available = false
		gate.finishExclusive(true, false)
		committed = true
		return snapshot, nil
	}

	if err := a.ensureStarted(ctx); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if a.codexRuntimeModeSnapshot() != CodexRuntimeWeClaw {
		return CodexRestartSnapshot{}, fmt.Errorf("%w: 当前 Host 权威不是 WeClaw", ErrCodexRuntimeUnavailable)
	}
	if !opts.ForceTerminateCodex {
		if err := a.requireCodexRestartIdle(ctx); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}

	lifecycleLock, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	defer releaseCodexHostStartupLock(lifecycleLock)
	if !stopConflictingCodexHosts {
		if err := a.requireCodexDesktopAbsent(); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}
	if !opts.ForceTerminateCodex {
		if err := a.requireCodexRestartIdle(ctx); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}
	metadata, err := a.validateManagedCodexHost(socketPath)
	if err != nil {
		return CodexRestartSnapshot{}, fmt.Errorf("验证待停止的 Codex Host: %w", err)
	}
	if err := a.preflightCodexHostConflicts(ctx, metadata.PID); err != nil {
		return CodexRestartSnapshot{}, err
	}
	snapshot = CodexRestartSnapshot{
		HostMode: strings.TrimSpace(a.codexHostMode), SocketPath: socketPath,
		HostGeneration: metadata.Generation, HostStopped: true,
	}
	if len(plannedConflicts) > 0 {
		stopped := make(map[int]bool, len(plannedConflicts))
		for _, conflict := range plannedConflicts {
			stopped[conflict.group.PGID] = true
		}
		snapshot.ConflictingHosts = restartConflictSnapshots(plannedConflicts, stopped)
	}
	// Persist the old generation before process mutation. If either process
	// crashes after this point, startup recovery can still prove replacement.
	if err := persistIntent(snapshot); err != nil {
		return CodexRestartSnapshot{}, fmt.Errorf("持久化 Codex Host 重启意图: %w", err)
	}
	if !stopConflictingCodexHosts {
		if err := a.requireCodexDesktopAbsent(); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}
	if !opts.ForceTerminateCodex {
		if err := a.requireCodexRestartIdle(ctx); err != nil {
			return CodexRestartSnapshot{}, err
		}
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

// stopExplicitCodexHostConflicts takes a second, type-specific identity proof
// for every extra Host. The complete plan is journaled before the first
// external process mutation; each successful stop is then journaled separately
// so an interrupted operation remains observable and recoverable.
func (a *ACPAgent) stopExplicitCodexHostConflicts(
	ctx context.Context,
	snapshot CodexRestartSnapshot,
	persistIntent func(CodexRestartSnapshot) error,
	force bool,
) (CodexRestartSnapshot, []codexHostConflictTarget, error) {
	authorityPID := 0
	if !force {
		authorityPID = a.codexRestartAuthorityPID(ctx, snapshot.SocketPath)
	}
	plan, err := a.planCodexHostConflicts(ctx, authorityPID)
	if err != nil {
		return CodexRestartSnapshot{}, nil, err
	}
	if len(plan.conflictGroups) == 0 {
		return snapshot, nil, nil
	}
	verified := make([]codexVerifiedHostConflictTarget, 0, len(plan.conflicts))
	for _, target := range plan.conflicts {
		if target.kind == codexHostConflictTargetUnknown && !force {
			return CodexRestartSnapshot{}, nil, fmt.Errorf(
				"%w：PGID %d（%s）的身份无法完整证明；显式参数不会停止未知进程",
				ErrCodexHostConflict, target.group.PGID, target.group.Kind,
			)
		}
		var candidate codexVerifiedHostConflictTarget
		var verifyErr error
		if force {
			candidate, verifyErr = a.captureCodexHostConflictTarget(ctx, target)
		} else {
			candidate, verifyErr = a.verifyCodexHostConflictTarget(ctx, target)
		}
		if verifyErr != nil {
			return CodexRestartSnapshot{}, nil, fmt.Errorf("复核待停止的 Codex Host PGID %d: %w", target.group.PGID, verifyErr)
		}
		verified = append(verified, candidate)
	}

	planned := make([]codexHostConflictTarget, 0, len(verified))
	for _, target := range verified {
		planned = append(planned, target.codexHostConflictTarget)
	}
	snapshot.ConflictingHosts = restartConflictSnapshots(planned, nil)
	if err := persistIntent(snapshot); err != nil {
		return CodexRestartSnapshot{}, nil, fmt.Errorf("持久化冲突 Codex Host 重启意图: %w", err)
	}
	a.codexRestartMu.Lock()
	a.codexRestartSnapshot = snapshot
	a.codexRestartPrepared = true
	a.codexRestartMu.Unlock()

	stopped := make(map[int]bool, len(verified))
	for _, target := range verified {
		var stopErr error
		if force {
			stopErr = a.stopForceCodexHostConflict(ctx, target)
		} else {
			stopErr = a.stopVerifiedCodexHostConflict(ctx, target)
		}
		if stopErr != nil {
			return CodexRestartSnapshot{}, nil, fmt.Errorf("%w: 停止冲突 Codex Host PGID %d: %v", ErrCodexRestartUnsafe, target.group.PGID, stopErr)
		}
		stopped[target.group.PGID] = true
		snapshot.ConflictingHosts = restartConflictSnapshots(planned, stopped)
		if err := persistIntent(snapshot); err != nil {
			return CodexRestartSnapshot{}, nil, fmt.Errorf("%w: 记录冲突 Codex Host PGID %d 已停止: %v", ErrCodexRestartUnsafe, target.group.PGID, err)
		}
		a.codexRestartMu.Lock()
		a.codexRestartSnapshot = snapshot
		a.codexRestartMu.Unlock()
	}
	return snapshot, planned, nil
}

// forceCloseCodexDesktopApp applies the operator's explicit --force authority
// to the Desktop frontend before any app-server process groups are planned.
// A remaining private Host is still discovered and identity-checked by the
// normal conflict snapshot immediately afterwards.
func (a *ACPAgent) forceCloseCodexDesktopApp(ctx context.Context) error {
	if !a.codexAppFrontendPresent() {
		return nil
	}
	if err := a.stopCodexDesktopProviderHost(ctx); err != nil {
		return fmt.Errorf("强制关闭 Codex App: %w", err)
	}
	if a.codexAppFrontendPresent() {
		return fmt.Errorf("%w；--force 已请求退出，但 Codex App 仍在运行", ErrCodexDesktopFrontendActive)
	}
	return nil
}

func (a *ACPAgent) stopForceCodexHostConflict(ctx context.Context, expected codexVerifiedHostConflictTarget) error {
	current, err := a.captureCodexHostConflictTarget(ctx, expected.codexHostConflictTarget)
	if err != nil {
		return err
	}
	if !sameCodexHostConflictProof(expected, current) {
		return fmt.Errorf("候选 Codex Host PGID %d 的 PID、UID、启动时间或命令指纹已变化", expected.group.PGID)
	}
	return a.stopCodexConflictProcessGroup(ctx, current)
}

func (a *ACPAgent) detachForceStoppedCodexHostClient() {
	connection, _, _ := a.disconnectCodexHostClient(true)
	if connection != nil {
		_ = connection.Close()
	}
	a.failAppServerActiveTurns("Codex app-server force stopped by operator")
	a.failPendingRequests("Codex app-server force stopped by operator")
	a.setCodexRuntimeMode(CodexRuntimeUnknown)
	if a.codexOwners != nil {
		a.codexOwners.invalidateRuntimeAuthority(CodexRuntimeWeClaw)
	}
}

// ForceCompleteCodexRestart escalates an already prepared restart whose Host
// stop outcome could not be established. The process snapshot, not the stale
// lifecycle record, is authoritative for this explicit operator action.
func (a *ACPAgent) ForceCompleteCodexRestart(
	ctx context.Context,
	persistIntent func(CodexRestartSnapshot) error,
) (CodexRestartSnapshot, error) {
	if a == nil || !a.usesCodexSharedHost() {
		return CodexRestartSnapshot{}, fmt.Errorf("当前 Agent 不是 Codex shared app-server")
	}
	if persistIntent == nil {
		return CodexRestartSnapshot{}, fmt.Errorf("缺少 Codex Host 重启意图持久化回调")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	a.codexRestartMu.Lock()
	prepared := a.codexRestartPrepared
	snapshot := a.codexRestartSnapshot
	a.codexRestartMu.Unlock()
	if !prepared {
		return CodexRestartSnapshot{}, fmt.Errorf("没有可强制收敛的 Codex Host 重启事务")
	}
	if err := a.forceCloseCodexDesktopApp(ctx); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if strings.TrimSpace(snapshot.SocketPath) == "" {
		socketPath, err := a.resolveCodexHostSocket()
		if err != nil {
			return CodexRestartSnapshot{}, err
		}
		snapshot.SocketPath = socketPath
	}
	if strings.TrimSpace(snapshot.HostMode) == "" {
		snapshot.HostMode = strings.TrimSpace(a.codexHostMode)
	}
	snapshot.HostStopped = true

	result, planned, err := a.stopExplicitCodexHostConflicts(ctx, snapshot, persistIntent, true)
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	if len(planned) == 0 {
		if err := persistIntent(result); err != nil {
			return CodexRestartSnapshot{}, fmt.Errorf("持久化 Codex Host 强制停止意图: %w", err)
		}
	}
	a.codexRestartMu.Lock()
	a.codexRestartSnapshot = result
	a.codexRestartPrepared = true
	a.codexRestartMu.Unlock()
	a.detachForceStoppedCodexHostClient()
	return result, nil
}

func (a *ACPAgent) codexRestartAuthorityPID(ctx context.Context, socketPath string) int {
	if a.isRuntimeStarted() {
		if pid := a.runtimePID(); pid > 0 {
			return pid
		}
	}
	if metadata, err := a.validateManagedCodexHost(socketPath); err == nil {
		return metadata.PID
	}
	if a.usesOfficialCodexDaemon() {
		if output, err := a.runAndValidateCodexDaemonLifecycle(ctx, "version", socketPath); err == nil {
			return output.PID
		}
	}
	return 0
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
	verified, err := a.verifyStartedCodexHost(ctx, previous, false, false)
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
	topologyChanged, err := a.codexRestartTopologyChanged(previous)
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	managedMigration := previous.HostStopped &&
		strings.TrimSpace(previous.HostMode) == codexHostModeDaemon &&
		(strings.TrimSpace(a.codexHostMode) == codexHostModeManaged || a.codexHostMode == codexHostModeShared)
	if topologyChanged && !a.usesOfficialCodexDaemon() && !managedMigration {
		return CodexRestartSnapshot{}, fmt.Errorf(
			"%w: 已停止的 Codex Host 拓扑发生变化，拒绝未经验证的自动迁移",
			ErrCodexRuntimeUnavailable,
		)
	}
	allowDaemonFrontend := (topologyChanged && a.usesOfficialCodexDaemon()) || a.codexHostMode == codexHostModeShared
	if !allowDaemonFrontend {
		if err := a.requireCodexDesktopAbsent(); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}
	if previous.HostStopped {
		currentMode := strings.TrimSpace(a.codexHostMode)
		if !topologyChanged && previous.HostMode != "" && currentMode != previous.HostMode {
			return CodexRestartSnapshot{}, fmt.Errorf(
				"%w: Codex Host mode 已从 %s 变为 %s",
				ErrCodexRuntimeUnavailable, previous.HostMode, currentMode,
			)
		}
		currentSocket, err := a.resolveCodexHostSocket()
		if err != nil {
			return CodexRestartSnapshot{}, err
		}
		if !topologyChanged && previous.SocketPath != "" && filepath.Clean(currentSocket) != filepath.Clean(previous.SocketPath) {
			return CodexRestartSnapshot{}, fmt.Errorf("%w: Codex Host socket 已变更", ErrCodexRuntimeUnavailable)
		}
	}
	return a.verifyStartedCodexHost(ctx, previous, topologyChanged, allowDaemonFrontend)
}

// codexRestartTopologyChanged reports whether a persisted stopped-Host intent
// describes a different mode or socket from the current configuration. Callers
// still prove the current Host identity and process group after startup. Only
// migration from a stopped official daemon to explicit managed mode is allowed
// in that direction, while only migration to the official daemon may keep a
// Desktop frontend attached.
func (a *ACPAgent) codexRestartTopologyChanged(previous CodexRestartSnapshot) (bool, error) {
	if !previous.HostStopped {
		return false, nil
	}
	currentMode := strings.TrimSpace(a.codexHostMode)
	currentSocket, err := a.resolveCodexHostSocket()
	if err != nil {
		return false, fmt.Errorf("%w: 无法解析当前 Codex Host 拓扑: %v", ErrCodexRuntimeUnavailable, err)
	}
	modeChanged := previous.HostMode != "" && currentMode != previous.HostMode
	socketChanged := previous.SocketPath != "" && filepath.Clean(currentSocket) != filepath.Clean(previous.SocketPath)
	return modeChanged || socketChanged, nil
}

func (a *ACPAgent) verifyStartedCodexHost(
	ctx context.Context,
	previous CodexRestartSnapshot,
	topologyChanged bool,
	allowDaemonFrontend bool,
) (CodexRestartSnapshot, error) {
	current, err := a.inspectStartedCodexHost(ctx, allowDaemonFrontend)
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	// A changed topology has a new authority and is validated by the current
	// daemon identity plus the multi-Host preflight. Never stop that current Host
	// merely because its numeric generation happens to match the old snapshot.
	if topologyChanged {
		return current, nil
	}
	if !previous.HostStopped || previous.HostGeneration == 0 || current.HostGeneration != previous.HostGeneration {
		return current, nil
	}
	if err := a.replaceStaleCodexHostGeneration(ctx, previous); err != nil {
		return CodexRestartSnapshot{}, err
	}
	current, err = a.inspectStartedCodexHost(ctx, false)
	if err != nil {
		return CodexRestartSnapshot{}, err
	}
	if current.HostGeneration == previous.HostGeneration {
		return CodexRestartSnapshot{}, fmt.Errorf("%w: Codex Host generation 未变化", ErrCodexRuntimeUnavailable)
	}
	return current, nil
}

func (a *ACPAgent) inspectStartedCodexHost(ctx context.Context, allowDaemonFrontend bool) (CodexRestartSnapshot, error) {
	allowDaemonFrontend = allowDaemonFrontend || a.codexHostMode == codexHostModeShared
	if err := a.ensureStarted(ctx); err != nil {
		return CodexRestartSnapshot{}, err
	}
	if !allowDaemonFrontend {
		if err := a.requireCodexDesktopAbsent(); err != nil {
			return CodexRestartSnapshot{}, err
		}
	}
	if allowDaemonFrontend && !a.usesOfficialCodexDaemon() && a.codexHostMode != codexHostModeShared {
		return CodexRestartSnapshot{}, fmt.Errorf(
			"%w: 仅官方 daemon 恢复允许 Codex App 前端保持运行",
			ErrCodexRuntimeUnavailable,
		)
	}
	if a.codexRuntimeModeSnapshot() != CodexRuntimeWeClaw {
		return CodexRestartSnapshot{}, fmt.Errorf("%w: 重启后 Host 权威不是 WeClaw", ErrCodexRuntimeUnavailable)
	}
	// A topology migration only attaches to the already verified official
	// daemon. It does not stop or replace that Host, so its active threads may
	// continue while WeClaw recovers the old transaction.
	if !allowDaemonFrontend || a.codexHostMode == codexHostModeShared {
		if err := a.requireCodexRestartIdle(ctx); err != nil {
			return CodexRestartSnapshot{}, err
		}
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

// reconnectExistingCodexHostForRestart refreshes a disconnected client before
// restart safety trusts its cached thread state. It never starts a replacement
// Host; it only reconciles protected metadata that proves the old Host is dead.
func (a *ACPAgent) reconnectExistingCodexHostForRestart(ctx context.Context) error {
	if a.isRuntimeStarted() || a.rpcCall != nil {
		return nil
	}
	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return err
	}
	exists, err := existingCodexHostSocket(socketPath)
	if err != nil {
		return err
	}
	if !exists {
		if _, repairErr := a.reconcileDeadCodexHostArtifactsForRestart(ctx, socketPath); repairErr != nil {
			return fmt.Errorf("收敛无 socket 的陈旧 Codex Host 状态: %w", repairErr)
		}
		return nil
	}
	if err := a.attachExistingSharedCodexHost(ctx, socketPath); err != nil {
		repaired, repairErr := a.reconcileDeadCodexHostArtifactsForRestart(ctx, socketPath)
		if repairErr != nil {
			return fmt.Errorf("重新连接已有 Codex Host 以复核 thread 状态失败（%v）；收敛陈旧 Host 状态: %w", err, repairErr)
		}
		if repaired {
			return nil
		}
		// No Host process has been mutated on this path. Keep the error outside
		// ErrCodexRestartUnsafe so the service can reopen admission instead of
		// claiming that a stop operation has an unknown outcome.
		return fmt.Errorf("重新连接已有 Codex Host 以复核 thread 状态: %w", err)
	}
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)
	return nil
}

// reconcileDeadCodexHostArtifactsForRestart repairs only a demonstrably dead
// Host: the protected metadata PID is absent, the socket has no listener, and
// the multi-Host preflight finds no replacement authority. A live or merely
// unverifiable process remains untouched and failed closed.
func (a *ACPAgent) reconcileDeadCodexHostArtifactsForRestart(ctx context.Context, socketPath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	lifecycleLock, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return false, err
	}
	defer releaseCodexHostStartupLock(lifecycleLock)

	metadata, err := a.readCodexHostMetadata(socketPath)
	if err != nil || metadata.State != "running" || metadata.PID <= 0 {
		return false, nil
	}
	if codexHostProcessAlive(metadata.PID) {
		return false, nil
	}
	if exists, existsErr := existingCodexHostSocket(socketPath); existsErr != nil {
		return false, existsErr
	} else if exists {
		listening, probeErr := codexHostSocketListening(ctx, socketPath)
		if probeErr != nil {
			return false, probeErr
		}
		if listening {
			return false, nil
		}
	}
	if err := a.preflightCodexHostConflicts(ctx, 0); err != nil {
		return false, err
	}
	// Recheck the endpoint after the process-table preflight. A listener that
	// appeared meanwhile is not stale and must never be removed.
	if exists, existsErr := existingCodexHostSocket(socketPath); existsErr != nil {
		return false, existsErr
	} else if exists {
		listening, probeErr := codexHostSocketListening(ctx, socketPath)
		if probeErr != nil {
			return false, probeErr
		}
		if listening {
			return false, nil
		}
	}
	if err := a.markCodexHostMetadataStoppedLocked(socketPath, metadata); err != nil {
		return false, err
	}
	updated, err := a.readCodexHostMetadata(socketPath)
	if err != nil {
		return false, err
	}
	if updated.State != "stopped" || !sameCodexHostGeneration(updated, metadata) {
		return false, nil
	}
	if err := a.removeStaleCodexHostSocket(socketPath); err != nil {
		return false, err
	}
	return true, nil
}

func codexHostSocketListening(ctx context.Context, socketPath string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, codexHostDialTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(probeCtx, "unix", socketPath)
	if err == nil {
		_ = conn.Close()
		return true, nil
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("探测 Codex Host socket 监听状态: %w", err)
}

func (a *ACPAgent) requireCodexRestartIdle(ctx context.Context) error {
	if a.codexOwners != nil {
		if count, uncertain := a.codexOwners.anyWriterLeaseStatus(); count > 0 {
			if uncertain {
				return fmt.Errorf("%w: 存在 %d 个终态未确认的 writer lease", ErrCodexWriterBusy, count)
			}
			return fmt.Errorf("%w: 存在 %d 个 writer lease", ErrCodexWriterBusy, count)
		}
	}
	authoritativeIdle := a.isRuntimeStarted() || a.rpcCall != nil
	if authoritativeIdle {
		idleCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := a.ensureAllCodexThreadsIdle(idleCtx); err != nil {
			return fmt.Errorf("%w: 无法确认所有 Codex thread 均为空闲: %v", ErrCodexWriterBusy, err)
		}
		if a.codexOwners != nil {
			a.codexOwners.confirmWeClawThreadsIdle()
		}
	}
	if a.codexOwners != nil {
		if active, unknown := a.codexOwners.anyActiveThreadStatus(); active > 0 || unknown {
			return fmt.Errorf("%w: 存在 %d 个活动 thread，unknown=%t", ErrCodexWriterBusy, active, unknown)
		}
	}
	return nil
}
