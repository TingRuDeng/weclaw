package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// codexRuntimeModeSnapshot 返回当前 Host 级写入权威。零值统一视为 unknown。
func (a *ACPAgent) codexRuntimeModeSnapshot() CodexRuntimeHolder {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !validCodexRuntimeHolder(a.codexRuntimeMode) {
		return CodexRuntimeUnknown
	}
	return a.codexRuntimeMode
}

// setCodexRuntimeMode 原子切换 Host 权威，并废止属于上一 Host 的 thread 快照。
func (a *ACPAgent) setCodexRuntimeMode(runtime CodexRuntimeHolder) {
	if !validCodexRuntimeHolder(runtime) {
		runtime = CodexRuntimeUnknown
	}
	a.mu.Lock()
	a.codexRuntimeMode = runtime
	a.mu.Unlock()
	if a.codexOwners != nil {
		a.codexOwners.switchRuntimeAuthority(runtime)
	}
}

func (a *ACPAgent) handleCodexDesktopDisconnect() {
	if a.codexRuntimeModeSnapshot() == CodexRuntimeDesktop {
		a.setCodexRuntimeMode(CodexRuntimeUnknown)
	}
}

// tryStartCodexDesktopRuntime 在默认 auto 拓扑中优先连接已运行的 Codex App。
// App 仍存在但 IPC 不可达时必须失败关闭，不能启动第二个 app-server。
func (a *ACPAgent) tryStartCodexDesktopRuntime(ctx context.Context) (bool, error) {
	if !a.codexDesktopBridge || a.desktopRuntime == nil {
		return false, nil
	}
	socketExists, processExists := a.desktopRuntime.Presence()
	if !socketExists && !processExists {
		return false, nil
	}
	if err := a.desktopRuntime.connect(ctx); err != nil {
		if desktopHostDefinitelyAbsent(a.desktopRuntime) {
			return false, nil
		}
		return false, fmt.Errorf("%w: Codex App 正在运行但安全 IPC 不可达: %v", ErrCodexDesktopOwnershipUnknown, err)
	}
	if err := a.transitionCodexRuntimeToDesktop(ctx); err != nil {
		_ = a.desktopRuntime.disconnect()
		return false, err
	}
	return true, nil
}

func desktopHostDefinitelyAbsent(probe codexDesktopOwnerProbe) bool {
	if probe == nil {
		return true
	}
	socketExists, processExists := probe.Presence()
	return !socketExists && !processExists
}

func (a *ACPAgent) requireCodexSharedHostCapability(operation string) error {
	if a.codexRuntimeModeSnapshot() != CodexRuntimeDesktop {
		return nil
	}
	return fmt.Errorf(
		"%w: Codex App 当前是唯一 Host，%s 必须在 Codex App 中执行",
		ErrCodexDesktopCapabilityUnavailable,
		operation,
	)
}

func (a *ACPAgent) transitionCodexRuntimeToDesktop(ctx context.Context) error {
	if a.codexRuntimeModeSnapshot() != CodexRuntimeDesktop {
		if err := a.stopSharedCodexHostForDesktop(ctx); err != nil {
			return err
		}
	}
	a.setCodexRuntimeMode(CodexRuntimeDesktop)
	return nil
}

// stopSharedCodexHostForDesktop 只在全局空闲、进程内无 writer lease 且持有
// lifecycle lock 时停止共享 Host。任何停止结果不确定都会让 gate 失败关闭。
func (a *ACPAgent) stopSharedCodexHostForDesktop(ctx context.Context) (err error) {
	gate := a.ensureCodexAppServerGate()
	if err := gate.beginExclusive(); err != nil {
		return err
	}
	committed, available := false, true
	defer func() { gate.finishExclusive(committed, available) }()

	if a.codexOwners != nil {
		if count, uncertain := a.codexOwners.anyWriterLeaseStatus(); count > 0 {
			if uncertain {
				return fmt.Errorf("%w: 共享 Host 存在终态未确认的写入任务", ErrCodexWriterBusy)
			}
			return ErrCodexWriterBusy
		}
	}
	mode := a.codexRuntimeModeSnapshot()
	if mode == CodexRuntimeWeClaw {
		if err := a.ensureAllCodexThreadsIdle(ctx); err != nil {
			return err
		}
	}
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
	defer releaseCodexHostStartupLock(lifecycleLock)
	if mode != CodexRuntimeWeClaw {
		exists, existsErr := existingCodexHostSocket(socketPath)
		if existsErr != nil {
			return existsErr
		}
		if !exists {
			return nil
		}
		if err := a.attachExistingSharedCodexHost(ctx, socketPath); err != nil {
			return fmt.Errorf("%w: 已存在的共享 Codex Host 无法安全接管并停止: %v", ErrCodexDesktopOwnershipUnknown, err)
		}
		a.setCodexRuntimeMode(CodexRuntimeWeClaw)
	}
	if err := a.ensureAllCodexThreadsIdle(ctx); err != nil {
		return err
	}

	available = false
	if err := a.stopManagedHost(ctx, socketPath); err != nil {
		return fmt.Errorf("停止共享 Codex Host: %w", err)
	}
	available = true
	committed = true
	return nil
}

func existingCodexHostSocket(socketPath string) (bool, error) {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查共享 Codex Host socket: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return false, fmt.Errorf("共享 Codex Host endpoint 不是安全 Unix socket: %s", socketPath)
	}
	return true, nil
}

// attachExistingSharedCodexHost 只连接已有且身份可验证的 Host，绝不在此路径启动新进程。
func (a *ACPAgent) attachExistingSharedCodexHost(ctx context.Context, socketPath string) error {
	metadata, err := a.validateManagedCodexHost(socketPath)
	if err != nil {
		return err
	}
	conn, err := dialCodexHost(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("连接已有共享 Codex Host: %w", err)
	}
	if err := a.attachCodexHostConnection(conn); err != nil {
		_ = conn.Close()
		return err
	}
	if _, err := a.initializeACPSubprocess(ctx, metadata.PID); err != nil {
		connection, _, _ := a.disconnectCodexHostClient(false)
		if connection != nil {
			_ = connection.Close()
		}
		return fmt.Errorf("初始化已有共享 Codex Host: %w", err)
	}
	return nil
}
