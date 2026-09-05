package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CodexCLILaunchOptions describes one interactive CLI frontend attached to the
// official shared app-server. Arguments are passed to Codex after --remote.
type CodexCLILaunchOptions struct {
	Cwd            string
	Args           []string
	AllowHostStart bool
}

// CodexCLILaunch is a fully resolved, shell-free process specification.
type CodexCLILaunch struct {
	Command    string
	Args       []string
	Cwd        string
	Env        []string
	SocketPath string
}

// CodexCLIHost identifies the shared official daemon prepared for interactive
// CLI frontends. It intentionally excludes process environment and credentials.
type CodexCLIHost struct {
	SocketPath string `json:"socket_path"`
}

// CodexCLIHostController lets the running WeClaw service perform the Host
// ownership checks before a separate terminal process connects.
type CodexCLIHostController interface {
	PrepareCodexCLIHost(context.Context) (CodexCLIHost, error)
}

// PrepareCodexCLIHost starts or validates the configured shared Host through
// the service-owned ACPAgent, preserving its resolved Host topology. Managed
// Hosts must be prepared here so a separate CLI process never owns their
// lifecycle.
func (a *ACPAgent) PrepareCodexCLIHost(ctx context.Context) (CodexCLIHost, error) {
	if a == nil || a.protocol != protocolCodexAppServer {
		return CodexCLIHost{}, fmt.Errorf("受控 Codex CLI 需要原生 app-server 配置")
	}
	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	if a.codexRuntimeModeSnapshot() == CodexRuntimeDesktop {
		return CodexCLIHost{}, fmt.Errorf("Codex App 当前是唯一 Host，不能准备共享 Codex CLI")
	}
	if err := a.ensureStarted(ctx); err != nil {
		return CodexCLIHost{}, fmt.Errorf("准备共享 Codex Host: %w", err)
	}
	permit, err := a.ensureCodexAppServerGate().acquire(ctx)
	if err != nil {
		return CodexCLIHost{}, err
	}
	defer permit.release()
	launch, err := a.PrepareCodexCLILaunch(ctx, CodexCLILaunchOptions{AllowHostStart: false})
	if err != nil {
		return CodexCLIHost{}, err
	}
	return CodexCLIHost{SocketPath: launch.SocketPath}, nil
}

// PrepareCodexCLILaunch validates the configured shared Host and pins the
// interactive Codex client to that exact Unix socket. The official daemon may
// be started directly when WeClaw is absent; a managed Host must already have
// been prepared by the running service so a CLI process cannot orphan it.
func (a *ACPAgent) PrepareCodexCLILaunch(ctx context.Context, opts CodexCLILaunchOptions) (CodexCLILaunch, error) {
	if a == nil || a.protocol != protocolCodexAppServer {
		return CodexCLILaunch{}, fmt.Errorf("受控 Codex CLI 需要原生 app-server 配置")
	}
	if err := a.validateCodexLiveTestPathIsolation(); err != nil {
		return CodexCLILaunch{}, err
	}
	if err := validateCodexCLIFrontendArgs(codexCLIFrontendPrefixArgs(a.args)); err != nil {
		return CodexCLILaunch{}, fmt.Errorf("Codex app-server 配置不能用于受控 CLI: %w", err)
	}
	if err := validateCodexCLIFrontendArgs(opts.Args); err != nil {
		return CodexCLILaunch{}, err
	}
	if a.runAs.shouldIsolate() {
		return CodexCLILaunch{}, fmt.Errorf("受控 Codex CLI 不支持 run_as_user")
	}
	officialDaemon := a.usesOfficialCodexDaemon()
	sharedApp := a.codexHostMode == codexHostModeShared
	if !officialDaemon && !sharedApp && opts.AllowHostStart {
		return CodexCLILaunch{}, fmt.Errorf("managed Codex Host 必须由运行中的 WeClaw 服务准备；请先启动 WeClaw")
	}
	command := a.command
	if officialDaemon || sharedApp {
		var err error
		command, err = a.resolveCodexDaemonLifecycleCommand()
		if err != nil {
			return CodexCLILaunch{}, err
		}
	}
	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return CodexCLILaunch{}, err
	}
	if sharedApp && opts.AllowHostStart {
		if _, err := a.launchCodexAppSharedClient(ctx); err != nil {
			return CodexCLILaunch{}, err
		}
		// This short-lived launcher owns only its connection; the signed host
		// launcher and the App keep the shared service alive.
		connection, _, _ := a.disconnectCodexHostClient(false)
		if connection != nil {
			_ = connection.Close()
		}
	}
	exists, err := existingCodexHostSocket(socketPath)
	if err != nil {
		return CodexCLILaunch{}, err
	}
	if !exists && !opts.AllowHostStart {
		return CodexCLILaunch{}, fmt.Errorf("WeClaw 正在运行但未使用 official daemon，已拒绝启动第二个 Codex Host；请先停止并重启 WeClaw")
	}
	if a.desktopProbe != nil && !sharedApp {
		socketExists, processExists := a.desktopProbe.Presence()
		if (socketExists || processExists) && a.codexRuntimeModeSnapshot() != CodexRuntimeWeClaw {
			return CodexCLILaunch{}, fmt.Errorf("Codex App 当前可见，无法证明 official daemon 是唯一 Host；受控 CLI 已拒绝连接")
		}
	}
	if err := a.prepareCodexHostSocket(socketPath); err != nil {
		return CodexCLILaunch{}, err
	}
	lock, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return CodexCLILaunch{}, err
	}
	defer releaseCodexHostStartupLock(lock)

	exists, err = existingCodexHostSocket(socketPath)
	if err != nil {
		return CodexCLILaunch{}, err
	}
	if officialDaemon {
		action := "version"
		if !exists {
			action = "start"
			if err := a.preflightCodexHostConflicts(ctx, 0); err != nil {
				return CodexCLILaunch{}, err
			}
		}
		output, err := a.runAndValidateCodexDaemonLifecycle(ctx, action, socketPath)
		if err != nil {
			return CodexCLILaunch{}, err
		}
		if filepath.Clean(output.ManagedCodexPath) != filepath.Clean(command) {
			return CodexCLILaunch{}, fmt.Errorf(
				"%w: managed Codex path=%s, expected=%s",
				errCodexDaemonUnmanaged,
				output.ManagedCodexPath,
				command,
			)
		}
		if err := a.preflightCodexHostConflicts(ctx, output.PID); err != nil {
			return CodexCLILaunch{}, err
		}
	} else if err := a.preflightConnectedManagedCodexHost(ctx, socketPath); err != nil {
		return CodexCLILaunch{}, err
	}

	env, err := mergeEnv(os.Environ(), a.env)
	if err != nil {
		return CodexCLILaunch{}, fmt.Errorf("build Codex CLI environment: %w", err)
	}
	cwd := strings.TrimSpace(opts.Cwd)
	if cwd == "" {
		cwd = a.cwd
	}
	prefixArgs := codexCLIFrontendPrefixArgs(a.args)
	args := prefixArgs
	if !codexCLIFrontendHasWorkingDirectoryArg(prefixArgs) && !codexCLIFrontendHasWorkingDirectoryArg(opts.Args) {
		args = append(args, "--cd", cwd)
	}
	args = append(args, "--remote", "unix://"+socketPath)
	args = append(args, opts.Args...)
	return CodexCLILaunch{
		Command: command, Args: args, Cwd: cwd, Env: env, SocketPath: socketPath,
	}, nil
}

func codexCLIFrontendHasWorkingDirectoryArg(args []string) bool {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "-C" || strings.HasPrefix(arg, "-C") || arg == "--cd" || strings.HasPrefix(arg, "--cd=") {
			return true
		}
	}
	return false
}

func codexCLIFrontendPrefixArgs(configured []string) []string {
	prefix := make([]string, 0, len(configured))
	for _, arg := range configured {
		if arg == "app-server" {
			break
		}
		prefix = append(prefix, arg)
	}
	return prefix
}

func validateCodexCLIFrontendArgs(args []string) error {
	for _, arg := range args {
		normalized := strings.ToLower(strings.TrimSpace(arg))
		if normalized == "--remote" || strings.HasPrefix(normalized, "--remote=") {
			return fmt.Errorf("--remote 由 WeClaw 固定，不能覆盖")
		}
		switch normalized {
		case "--",
			"exec", "e", "review", "login", "logout", "mcp", "plugin", "mcp-server",
			"app-server", "remote-control", "app", "completion", "update", "doctor",
			"sandbox", "debug", "apply", "a", "cloud", "exec-server", "features",
			"help", "delete", "unarchive":
			return fmt.Errorf("受控 Codex CLI 不允许 %q；仅支持交互 TUI 及其 resume/fork/archive 操作", arg)
		}
	}
	return nil
}
