package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
)

var ErrCodexAppRestartRequired = errors.New("Codex App 需要重启后才能复用官方 daemon")

type codexAppDaemonReuseResult struct {
	Changed          bool
	AppRunning       bool
	PrivateAppServer bool
}

// applyCodexAppDaemonReusePreference 只在用户显式关闭协调时撤销 App 启动环境。
// 启用必须等官方 daemon 已验证并可连接后再提交，避免 App 在 daemon 尚未就绪时
// 回退到自己的 stdio app-server。
func (a *ACPAgent) applyCodexAppDaemonReusePreference(ctx context.Context) error {
	if a.protocol != protocolCodexAppServer || a.codexAppReuseDaemon == nil || *a.codexAppReuseDaemon {
		return nil
	}
	result, err := a.configureCodexAppDaemonReuse(ctx, false, "")
	if err != nil {
		return fmt.Errorf("撤销 Codex App daemon 复用环境: %w", err)
	}
	if result.Changed {
		log.Printf("[codex-host] disabled Codex App reuse of the official daemon for future App launches")
	}
	if result.AppRunning {
		log.Printf("[codex-host] Codex App is already running; the disabled reuse setting takes effect after the App restarts")
	}
	return nil
}

// ensureCodexAppReusesDaemon 在官方 daemon 已通过身份和版本验证后，才允许
// 后续启动的 App 连接 control socket。已经运行私有 app-server 的 App 不会被
// WeClaw 退出；当前 Agent 失败关闭并要求用户重启 App。
func (a *ACPAgent) ensureCodexAppReusesDaemon(ctx context.Context, socketPath string) error {
	if !a.usesOfficialCodexDaemon() || a.codexAppReuseDaemon == nil || !*a.codexAppReuseDaemon {
		return nil
	}
	result, err := a.configureCodexAppDaemonReuse(ctx, true, socketPath)
	if err != nil {
		return fmt.Errorf("配置 Codex App 复用官方 daemon: %w", err)
	}
	if result.Changed {
		log.Printf("[codex-host] enabled Codex App reuse of the official daemon for future App launches")
	}
	return codexAppDaemonReuseResultError(result)
}

// validateRunningCodexAppDaemonReuse 供晚启动 App 的拓扑调和调用。它只检查
// 当前进程树与受保护 Desktop endpoint，不重复执行 launchctl 写操作。
func (a *ACPAgent) validateRunningCodexAppDaemonReuse(ctx context.Context) error {
	if !a.usesOfficialCodexDaemon() || a.codexAppReuseDaemon == nil || !*a.codexAppReuseDaemon {
		return nil
	}
	inspect := a.codexAppDaemonInspectCall
	if inspect == nil {
		inspect = inspectSystemCodexAppDaemonReuse
	}
	result, err := inspect(ctx)
	if err != nil {
		return fmt.Errorf("复核 Codex App daemon 复用状态: %w", err)
	}
	return codexAppDaemonReuseResultError(result)
}

func codexAppDaemonReuseResultError(result codexAppDaemonReuseResult) error {
	if !result.PrivateAppServer {
		return nil
	}
	return fmt.Errorf(
		"%w；WeClaw 未退出当前 App，请完整退出并重新打开 Codex App 后重试；若重启后仍失败，请同步更新 Codex App 与 standalone CLI",
		ErrCodexAppRestartRequired,
	)
}

func (a *ACPAgent) configureCodexAppDaemonReuse(
	ctx context.Context,
	enabled bool,
	socketPath string,
) (codexAppDaemonReuseResult, error) {
	if a.codexAppDaemonReuseCall != nil {
		return a.codexAppDaemonReuseCall(ctx, enabled, socketPath)
	}
	return configureSystemCodexAppDaemonReuse(ctx, enabled, socketPath)
}
