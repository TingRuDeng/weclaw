package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

// ErrOfflineRuntimeRestartIncomplete 表示已记录强制停止意图，不能恢复旧二进制绕过启动门禁。
var ErrOfflineRuntimeRestartIncomplete = errors.New("离线强制重启未完成")

// PrepareOfflineRuntimeRestart 用于没有协调接口的旧服务及离线重试。
// 调用方必须持有 frontend/启动租约；stopRuntime 返回前必须确认旧服务退出并取得运行锁。
// 在任何进程变更前记录 pending，失败时保留记录，只有完整停止后才允许启动恢复。
func PrepareOfflineRuntimeRestart(ctx context.Context, stopRuntime func() error, controller codexRestartOptionsController, serviceManager string) (resultErr error) {
	h := &Handler{}
	previous, exists, err := h.readRuntimeRestartState()
	if err != nil {
		return err
	}
	if exists && (previous.PreparedAt.IsZero() || (previous.Version != runtimeRestartStateVersion && !(previous.Version == 2 && previous.OfflineForcePending))) {
		return fmt.Errorf("重启事务状态无效，无法继续强制停止")
	}
	if previous.Codex && controller == nil {
		return fmt.Errorf("上次强制停止涉及 Codex Host，但当前配置没有 Codex Agent")
	}
	if stopRuntime == nil {
		return fmt.Errorf("离线重启缺少运行锁保护")
	}
	state := previous
	if serviceManager != "" {
		state.ServiceManager = serviceManager
	}
	// 旧版恢复器必须拒绝不认识的 pending 版本，不能忽略新字段后开放消息准入。
	state.Version, state.PreparedAt, state.OfflineForcePending = 2, time.Now().UTC(), true
	state.Codex = controller != nil
	if err := h.writeRuntimeRestartState(state); err != nil {
		return fmt.Errorf("记录离线强制停止意图: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(ErrOfflineRuntimeRestartIncomplete, resultErr)
		}
	}()
	if err := stopRuntime(); err != nil {
		return err
	}
	if controller != nil {
		persist := func(snapshot agent.CodexRestartSnapshot) error {
			if previous.CodexHost.HostGeneration != 0 {
				if snapshot.SocketPath != previous.CodexHost.SocketPath {
					return fmt.Errorf("Codex socket 与上次停止记录不一致，请恢复原配置后重试")
				}
				snapshot.HostGeneration = previous.CodexHost.HostGeneration
			}
			state.CodexHost = snapshot
			return h.writeRuntimeRestartState(state)
		}
		snapshot, err := controller.PrepareCodexRestartWithOptions(ctx, persist, agent.CodexRestartOptions{ForceTerminateCodex: true})
		if err != nil {
			return fmt.Errorf("离线 Codex 强制停止未完成，请使用 --force 重试: %w", err)
		}
		if !snapshot.HostStopped {
			return fmt.Errorf("离线 Codex Host 停止未确认")
		}
		for _, host := range snapshot.ConflictingHosts {
			if !host.Stopped {
				return fmt.Errorf("离线 Codex Host PGID %d 停止未确认", host.PGID)
			}
		}
		if err := persist(snapshot); err != nil {
			return err
		}
	}
	state.Version, state.OfflineForcePending = runtimeRestartStateVersion, false
	return h.writeRuntimeRestartState(state)
}

// RuntimeRestartPending 在新服务完成启动前 Host 验证并删除事务记录后返回 false。
func RuntimeRestartPending() (bool, error) {
	_, exists, err := (&Handler{}).readRuntimeRestartState()
	return exists, err
}

// RuntimeRestartServiceManager 保留旧服务退出后重试所需的启动方式。
func RuntimeRestartServiceManager() (string, error) {
	state, _, err := (&Handler{}).readRuntimeRestartState()
	if err != nil {
		return "", err
	}
	if state.ServiceManager != "" && state.ServiceManager != "systemd" {
		return "", fmt.Errorf("未知的重启服务管理方式")
	}
	return state.ServiceManager, nil
}
