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

// PrepareCodexCLIHost starts or validates the official daemon through the
// service-owned ACPAgent, preserving its resolved Host topology.
func (a *ACPAgent) PrepareCodexCLIHost(ctx context.Context) (CodexCLIHost, error) {
	if a == nil || a.protocol != protocolCodexAppServer {
		return CodexCLIHost{}, fmt.Errorf("受控 Codex CLI 需要原生 app-server 配置")
	}
	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	if a.codexRuntimeModeSnapshot() == CodexRuntimeDesktop {
		return CodexCLIHost{}, fmt.Errorf("Codex App 当前是唯一 Host，不能准备 official daemon CLI")
	}
	permit, err := a.ensureCodexAppServerGate().acquire(ctx)
	if err != nil {
		return CodexCLIHost{}, err
	}
	defer permit.release()
	launch, err := a.PrepareCodexCLILaunch(ctx, CodexCLILaunchOptions{AllowHostStart: true})
	if err != nil {
		return CodexCLIHost{}, err
	}
	return CodexCLIHost{SocketPath: launch.SocketPath}, nil
}

// PrepareCodexCLILaunch starts or validates the official daemon and pins the
// interactive Codex client to that exact Unix socket. A running WeClaw process
// passes AllowHostStart=false so this command cannot create a second Host.
func (a *ACPAgent) PrepareCodexCLILaunch(ctx context.Context, opts CodexCLILaunchOptions) (CodexCLILaunch, error) {
	if a == nil || a.protocol != protocolCodexAppServer {
		return CodexCLILaunch{}, fmt.Errorf("受控 Codex CLI 需要原生 app-server 配置")
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
	if !a.usesOfficialCodexDaemon() {
		return CodexCLILaunch{}, fmt.Errorf("受控 Codex CLI 只支持 official standalone daemon；当前是 managed 兼容 Host")
	}
	command, err := a.resolveCodexDaemonLifecycleCommand()
	if err != nil {
		return CodexCLILaunch{}, err
	}
	socketPath, err := a.resolveCodexDaemonSocket()
	if err != nil {
		return CodexCLILaunch{}, err
	}
	exists, err := existingCodexHostSocket(socketPath)
	if err != nil {
		return CodexCLILaunch{}, err
	}
	if !exists && !opts.AllowHostStart {
		return CodexCLILaunch{}, fmt.Errorf("WeClaw 正在运行但未使用 official daemon，已拒绝启动第二个 Codex Host；请先停止并重启 WeClaw")
	}
	if a.desktopProbe != nil {
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
	action := "version"
	if !exists {
		action = "start"
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

	env, err := mergeEnv(os.Environ(), a.env)
	if err != nil {
		return CodexCLILaunch{}, fmt.Errorf("build Codex CLI environment: %w", err)
	}
	cwd := strings.TrimSpace(opts.Cwd)
	if cwd == "" {
		cwd = a.cwd
	}
	args := append(codexCLIFrontendPrefixArgs(a.args), "--remote", "unix://"+socketPath)
	args = append(args, opts.Args...)
	return CodexCLILaunch{
		Command: command, Args: args, Cwd: cwd, Env: env, SocketPath: socketPath,
	}, nil
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
