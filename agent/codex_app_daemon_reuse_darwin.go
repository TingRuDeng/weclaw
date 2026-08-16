//go:build darwin

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	codexAppUseLocalDaemonEnv = "CODEX_APP_SERVER_USE_LOCAL_DAEMON"
	codexAppForceCLIEnv       = "CODEX_APP_SERVER_FORCE_CLI"
	codexCLIPathEnv           = "CODEX_CLI_PATH"
	codexAppLaunchctlTimeout  = 3 * time.Second
)

type codexDesktopHostProcessState struct {
	AppRunning       bool
	PrivateAppServer bool
}

type codexAppDaemonReuseDeps struct {
	launchEnvironment func(context.Context, string) (string, error)
	launchctl         func(context.Context, ...string) (string, error)
	inspect           func(context.Context) (codexAppDaemonReuseResult, error)
	userHome          func() (string, error)
}

func configureSystemCodexAppDaemonReuse(
	ctx context.Context,
	enabled bool,
	socketPath string,
) (codexAppDaemonReuseResult, error) {
	return configureCodexAppDaemonReuseWithDeps(ctx, enabled, socketPath, codexAppDaemonReuseDeps{
		launchEnvironment: codexAppLaunchEnvironment,
		launchctl:         runCodexAppLaunchctl,
		inspect:           inspectSystemCodexAppDaemonReuse,
		userHome:          os.UserHomeDir,
	})
}

func configureCodexAppDaemonReuseWithDeps(
	ctx context.Context,
	enabled bool,
	socketPath string,
	deps codexAppDaemonReuseDeps,
) (codexAppDaemonReuseResult, error) {
	commandCtx, cancel := context.WithTimeout(ctx, codexAppLaunchctlTimeout)
	defer cancel()
	current, err := deps.launchEnvironment(commandCtx, codexAppUseLocalDaemonEnv)
	if err != nil {
		return codexAppDaemonReuseResult{}, err
	}
	result := codexAppDaemonReuseResult{}
	if !enabled {
		if current != "" {
			if _, err := deps.launchctl(commandCtx, "unsetenv", codexAppUseLocalDaemonEnv); err != nil {
				return result, err
			}
			result.Changed = true
		}
		inspected, inspectErr := deps.inspect(ctx)
		result.AppRunning = inspected.AppRunning
		result.PrivateAppServer = inspected.PrivateAppServer
		return result, inspectErr
	}
	for _, conflict := range []struct {
		name  string
		value string
	}{
		{name: codexAppForceCLIEnv, value: "1"},
		{name: codexCLIPathEnv},
	} {
		value, valueErr := deps.launchEnvironment(commandCtx, conflict.name)
		if valueErr != nil {
			return result, valueErr
		}
		if value != "" && (conflict.value == "" || value == conflict.value) {
			return result, fmt.Errorf(
				"launchd environment %s=%q prevents Codex App daemon reuse; remove that override first",
				conflict.name,
				value,
			)
		}
	}
	appSocketPath, err := codexAppDaemonSocketFromLaunchEnvironment(commandCtx, deps)
	if err != nil {
		return result, err
	}
	if filepath.Clean(appSocketPath) != filepath.Clean(socketPath) {
		return result, fmt.Errorf(
			"Codex App daemon socket %s does not match WeClaw official daemon socket %s; align CODEX_HOME first",
			appSocketPath,
			socketPath,
		)
	}
	if current != "1" {
		if _, err := deps.launchctl(commandCtx, "setenv", codexAppUseLocalDaemonEnv, "1"); err != nil {
			return result, err
		}
		result.Changed = true
	}
	inspected, err := deps.inspect(ctx)
	result.AppRunning = inspected.AppRunning
	result.PrivateAppServer = inspected.PrivateAppServer
	return result, err
}

func inspectSystemCodexAppDaemonReuse(ctx context.Context) (codexAppDaemonReuseResult, error) {
	state, err := codexDesktopHostProcessStateFromSystem()
	result := codexAppDaemonReuseResult{
		AppRunning: state.AppRunning, PrivateAppServer: state.PrivateAppServer,
	}
	if err != nil {
		return result, err
	}
	if state.AppRunning && !state.PrivateAppServer {
		probeCtx, cancelProbe := context.WithTimeout(ctx, codexDesktopPresenceTimeout)
		defer cancelProbe()
		connected, probeErr := codexDesktopEndpointConnectableWithDeps(probeCtx, systemCodexDesktopEndpointDeps())
		if probeErr != nil {
			return result, fmt.Errorf("verify running Codex App Desktop endpoint: %w", probeErr)
		}
		if !connected {
			return result, fmt.Errorf("running Codex App has not exposed a verifiable Desktop endpoint")
		}
	}
	return result, nil
}

func codexAppDaemonSocketFromLaunchEnvironment(ctx context.Context, deps codexAppDaemonReuseDeps) (string, error) {
	codexHome, err := deps.launchEnvironment(ctx, "CODEX_HOME")
	if err != nil {
		return "", err
	}
	if codexHome == "" {
		home, homeErr := deps.userHome()
		if homeErr != nil {
			return "", fmt.Errorf("resolve Codex App home: %w", homeErr)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(codexHome) {
		return "", fmt.Errorf("Codex App CODEX_HOME must be absolute: %s", codexHome)
	}
	return codexDaemonSocketPath(filepath.Clean(codexHome)), nil
}

func codexAppLaunchEnvironment(ctx context.Context, name string) (string, error) {
	output, err := runCodexAppLaunchctl(ctx, "getenv", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func runCodexAppLaunchctl(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	stdout := &boundedCommandOutput{limit: codexCLICommandOutputLimit}
	stderr := &boundedCommandOutput{limit: codexCLICommandOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			return "", fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, detail)
	}
	return stdout.String(), nil
}

func codexDesktopHostProcessStateFromSystem() (codexDesktopHostProcessState, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return codexDesktopHostProcessState{}, fmt.Errorf("inspect Codex App process tree: %w", err)
	}
	return codexDesktopHostProcessStateFrom(processes, func(pid int) (string, error) {
		return psProcessField(pid, "command=")
	})
}

func codexDesktopHostProcessStateFrom(
	processes []unix.KinfoProc,
	command func(int) (string, error),
) (codexDesktopHostProcessState, error) {
	uid := os.Getuid()
	parents := make(map[int]int, len(processes))
	appPIDs := make(map[int]struct{})
	for _, process := range processes {
		if int(process.Eproc.Ucred.Uid) != uid {
			continue
		}
		pid := int(process.Proc.P_pid)
		parents[pid] = int(process.Eproc.Ppid)
		if codexDesktopProcessName(codexDesktopProcessCommand(process.Proc.P_comm)) {
			appPIDs[pid] = struct{}{}
		}
	}
	state := codexDesktopHostProcessState{AppRunning: len(appPIDs) > 0}
	if !state.AppRunning {
		return state, nil
	}
	for _, process := range processes {
		if int(process.Eproc.Ucred.Uid) != uid ||
			strings.ToLower(codexDesktopProcessCommand(process.Proc.P_comm)) != "codex" {
			continue
		}
		pid := int(process.Proc.P_pid)
		if !codexProcessDescendsFrom(pid, parents, appPIDs) {
			continue
		}
		fullCommand, err := command(pid)
		if err != nil {
			return state, fmt.Errorf("inspect Codex App child process %d: %w", pid, err)
		}
		if codexPrivateAppServerCommand(fullCommand) {
			state.PrivateAppServer = true
			return state, nil
		}
	}
	return state, nil
}

func codexProcessDescendsFrom(pid int, parents map[int]int, ancestors map[int]struct{}) bool {
	seen := make(map[int]struct{})
	for parent := parents[pid]; parent > 0; parent = parents[parent] {
		if _, ok := ancestors[parent]; ok {
			return true
		}
		if _, ok := seen[parent]; ok {
			return false
		}
		seen[parent] = struct{}{}
	}
	return false
}

func codexPrivateAppServerCommand(command string) bool {
	fields := strings.Fields(command)
	for index, field := range fields {
		if field != "app-server" {
			continue
		}
		if index+1 < len(fields) && (fields[index+1] == "daemon" || fields[index+1] == "proxy") {
			return false
		}
		return true
	}
	return false
}
