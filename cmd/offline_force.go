package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
)

type offlineCodexController interface {
	PrepareCodexRestartWithOptions(context.Context, func(agent.CodexRestartSnapshot) error, agent.CodexRestartOptions) (agent.CodexRestartSnapshot, error)
}

func configuredOfflineCodexController(ctx context.Context, cfg *config.Config) (offlineCodexController, error) {
	for _, candidate := range cfg.Agents {
		if isCodexAppServerAgent(candidate) {
			name, selected, err := configuredCodexAppServer(cfg)
			if err != nil {
				return nil, err
			}
			return agent.NewACPAgent(acpAgentConfigFromConfig(name, selected)), nil
		}
	}
	// 磁盘配置可能已移除旧服务仍在使用的 Codex，不能把缺少配置当作 Host 已退出。
	socket, process := agent.CodexDesktopFrontendPresence()
	if socket || process {
		return nil, fmt.Errorf("Codex App 仍在运行，但当前配置已没有 Codex app-server Agent；请恢复原 Codex 配置后使用 --force 重试")
	}
	if err := (&agent.ACPAgent{}).RequireNoCodexHosts(ctx); err != nil {
		return nil, fmt.Errorf("当前配置已没有 Codex app-server Agent，无法确认旧 Host 已退出；请恢复原 Codex 配置后使用 --force 重试: %w", err)
	}
	return nil, nil
}

// offlineForceRuntime 在 WeClaw 服务未运行时接管离线强制停止与可选重启恢复。
// frontend 租约持续到新服务验证完成；运行锁确保不会与并发启动的服务竞争。
func offlineForceRuntime(ctx context.Context, cfg *config.Config, start func(bool) error) error {
	lease, err := agent.AcquireCodexRestartLease()
	if err != nil {
		return fmt.Errorf("无法开始离线强制操作，请先退出受控 weclaw codex cli: %w", err)
	}
	defer lease.Close()
	return offlineForceRuntimeWithLease(ctx, cfg, start, configuredOfflineCodexController)
}

func offlineForceRuntimeWithLease(
	ctx context.Context,
	cfg *config.Config,
	start func(bool) error,
	selectController func(context.Context, *config.Config) (offlineCodexController, error),
) (resultErr error) {
	if cfg == nil {
		return fmt.Errorf("离线强制操作缺少配置")
	}
	launchLock, err := acquireDaemonLaunchLock()
	if err != nil {
		return err
	}
	var launch io.Closer = launchLock
	var runtime io.Closer
	defer func() {
		if runtime != nil {
			_ = runtime.Close()
		}
		if launch != nil {
			_ = launch.Close()
		}
	}()
	controller, err := selectController(ctx, cfg)
	if err != nil {
		return err
	}
	manager, err := messaging.RuntimeRestartServiceManager()
	if err != nil {
		return err
	}
	ensureOffline := func() error {
		lock, err := acquireRuntimeLock()
		if err != nil {
			return fmt.Errorf("无法确认服务已退出，尚未停止 Codex Host: %w", err)
		}
		runtime = lock
		if controller == nil {
			// 在取得运行锁后再次确认，防止并发退出前刚创建的 Host 被漏过。
			if _, err := selectController(ctx, cfg); err != nil {
				return err
			}
		}
		return nil
	}
	err = messaging.PrepareOfflineRuntimeRestart(ctx, ensureOffline, controller, manager)
	if err != nil {
		return err
	}
	if start == nil {
		return nil
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(messaging.ErrOfflineRuntimeRestartIncomplete, resultErr)
		}
	}()
	if err := runtime.Close(); err != nil {
		runtime = nil
		return err
	}
	runtime = nil
	if err := launch.Close(); err != nil {
		launch = nil
		return err
	}
	launch = nil
	if err := start(manager == "systemd"); err != nil {
		return err
	}
	return waitOfflineRestartRecovery(ctx)
}

func waitOfflineRestartRecovery(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	observedRunning := false
	for {
		pending, err := messaging.RuntimeRestartPending()
		if err != nil {
			return err
		}
		running := weclawIsRunningForRestart()
		if observedRunning && !running {
			return fmt.Errorf("新服务未保持运行，请查看 weclaw.log；重启记录已保留")
		}
		observedRunning = observedRunning || running
		if running && !pending {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待新服务完成 Codex Host 验证: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
