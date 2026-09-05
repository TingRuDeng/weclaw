//go:build darwin

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	codexAppUseLocalDaemonEnv = "CODEX_APP_SERVER_USE_LOCAL_DAEMON"
	codexAppForceCLIEnv       = "CODEX_APP_SERVER_FORCE_CLI"
	codexCLIPathEnv           = "CODEX_CLI_PATH"
	codexAppCodexHomeEnv      = "CODEX_HOME"
	codexAppLaunchctlTimeout  = 3 * time.Second
)

type codexDesktopHostProcessState struct {
	AppRunning       bool
	PrivateAppServer bool
	AppPIDs          []int
}

type codexAppDaemonInspectDeps struct {
	hostState           func() (codexDesktopHostProcessState, error)
	processEnvironment  func(int, string) (string, bool, error)
	launchEnvironment   func(context.Context, string) (string, error)
	userHome            func() (string, error)
	expectedEnvironment *codexAppDaemonEnvironment
	inspectionContext   context.Context
}

type codexAppDaemonReuseDeps struct {
	launchEnvironment   func(context.Context, string) (string, error)
	launchctl           func(context.Context, ...string) (string, error)
	inspect             func(context.Context) (codexAppDaemonReuseResult, error)
	userHome            func() (string, error)
	expectedEnvironment *codexAppDaemonEnvironment
}

type codexAppDaemonLaunchMutation struct {
	apply    []string
	rollback []string
}

func (e codexAppDaemonEnvironment) effectiveSQLiteHome() string {
	if e.CodexSQLiteHome != "" {
		return e.CodexSQLiteHome
	}
	return e.CodexHome
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

func configureSystemCodexAppDaemonReuseWithExpected(
	ctx context.Context,
	enabled bool,
	socketPath string,
	expected codexAppDaemonEnvironment,
) (codexAppDaemonReuseResult, error) {
	return configureCodexAppDaemonReuseWithDeps(ctx, enabled, socketPath, codexAppDaemonReuseDeps{
		launchEnvironment: codexAppLaunchEnvironment,
		launchctl:         runCodexAppLaunchctl,
		inspect: func(inspectCtx context.Context) (codexAppDaemonReuseResult, error) {
			return inspectSystemCodexAppDaemonReuseWithExpected(inspectCtx, &expected)
		},
		userHome:            os.UserHomeDir,
		expectedEnvironment: &expected,
	})
}

func configureCodexAppDaemonReuseWithDeps(
	ctx context.Context,
	enabled bool,
	socketPath string,
	deps codexAppDaemonReuseDeps,
) (codexAppDaemonReuseResult, error) {
	if deps.launchEnvironment == nil || deps.launchctl == nil || deps.inspect == nil {
		return codexAppDaemonReuseResult{}, fmt.Errorf("Codex App launchd dependencies are incomplete")
	}
	commandCtx, cancel := context.WithTimeout(ctx, codexAppLaunchctlTimeout)
	defer cancel()
	current, err := deps.launchEnvironment(commandCtx, codexAppUseLocalDaemonEnv)
	if err != nil {
		return codexAppDaemonReuseResult{}, err
	}
	current = strings.TrimSpace(current)
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
	mutations := make([]codexAppDaemonLaunchMutation, 0, 3)
	if expected := deps.expectedEnvironment; expected != nil {
		if deps.userHome == nil {
			return result, fmt.Errorf("Codex App user-home dependency is incomplete")
		}
		if err := expected.validate(); err != nil {
			return result, fmt.Errorf("validate Codex App shared environment: %w", err)
		}
		expectedSocket := codexDaemonSocketPath(expected.CodexHome)
		if filepath.Clean(expectedSocket) != filepath.Clean(socketPath) {
			return result, fmt.Errorf(
				"WeClaw official daemon socket %s does not match resolved CODEX_HOME socket %s",
				socketPath,
				expectedSocket,
			)
		}

		launchHomeRaw, homeErr := deps.launchEnvironment(commandCtx, codexAppCodexHomeEnv)
		if homeErr != nil {
			return result, homeErr
		}
		launchHome, homeErr := codexAppHomeFromLaunchValue(launchHomeRaw, deps.userHome)
		if homeErr != nil {
			return result, homeErr
		}
		plannedHome := launchHome
		if launchHome != expected.CodexHome {
			if strings.TrimSpace(launchHomeRaw) != "" {
				return result, fmt.Errorf(
					"launchd environment %s=%q resolves to %s, want %s; refusing to overwrite explicit Codex home",
					codexAppCodexHomeEnv,
					strings.TrimSpace(launchHomeRaw),
					launchHome,
					expected.CodexHome,
				)
			}
			mutations = append(mutations, codexAppDaemonLaunchMutation{
				apply:    []string{"setenv", codexAppCodexHomeEnv, expected.CodexHome},
				rollback: codexAppDaemonLaunchRestoreArgs(codexAppCodexHomeEnv, launchHomeRaw),
			})
			plannedHome = expected.CodexHome
		}

		launchSQLiteRaw, sqliteErr := deps.launchEnvironment(commandCtx, codexAppSQLiteHomeEnv)
		if sqliteErr != nil {
			return result, sqliteErr
		}
		launchSQLite, sqliteErr := normalizeOptionalCodexEnvironmentPath(codexAppSQLiteHomeEnv, launchSQLiteRaw)
		if sqliteErr != nil {
			return result, sqliteErr
		}
		if launchSQLite == "" {
			launchSQLite = plannedHome
		}
		expectedSQLite := expected.effectiveSQLiteHome()
		if launchSQLite != expectedSQLite {
			if strings.TrimSpace(launchSQLiteRaw) == "" && expected.CodexSQLiteHome != "" {
				mutations = append(mutations, codexAppDaemonLaunchMutation{
					apply:    []string{"setenv", codexAppSQLiteHomeEnv, expected.CodexSQLiteHome},
					rollback: codexAppDaemonLaunchRestoreArgs(codexAppSQLiteHomeEnv, launchSQLiteRaw),
				})
			} else {
				return result, fmt.Errorf(
					"launchd environment %s resolves to %s, want %s; refusing to overwrite explicit SQLite home",
					codexAppSQLiteHomeEnv,
					launchSQLite,
					expectedSQLite,
				)
			}
		}
	} else {
		appSocketPath, socketErr := codexAppDaemonSocketFromLaunchEnvironment(commandCtx, deps)
		if socketErr != nil {
			return result, socketErr
		}
		if filepath.Clean(appSocketPath) != filepath.Clean(socketPath) {
			return result, fmt.Errorf(
				"Codex App daemon socket %s does not match WeClaw official daemon socket %s; align CODEX_HOME first",
				appSocketPath,
				socketPath,
			)
		}
	}
	if current != "1" {
		mutations = append(mutations, codexAppDaemonLaunchMutation{
			apply:    []string{"setenv", codexAppUseLocalDaemonEnv, "1"},
			rollback: codexAppDaemonLaunchRestoreArgs(codexAppUseLocalDaemonEnv, current),
		})
	}
	for index, mutation := range mutations {
		if _, err := deps.launchctl(commandCtx, mutation.apply...); err != nil {
			rollbackErr := rollbackCodexAppDaemonLaunchMutations(ctx, deps.launchctl, mutations[:index+1])
			if rollbackErr != nil {
				return result, errors.Join(
					fmt.Errorf("apply launchd Codex App environment: %w", err),
					fmt.Errorf("rollback launchd Codex App environment: %w", rollbackErr),
				)
			}
			return result, fmt.Errorf("apply launchd Codex App environment: %w", err)
		}
	}
	result.Changed = len(mutations) > 0
	inspected, err := deps.inspect(ctx)
	result.AppRunning = inspected.AppRunning
	result.PrivateAppServer = inspected.PrivateAppServer
	return result, err
}

func inspectSystemCodexAppDaemonReuse(_ context.Context) (codexAppDaemonReuseResult, error) {
	return inspectCodexAppDaemonReuseWithDeps(codexAppDaemonInspectDeps{
		hostState:          codexDesktopHostProcessStateFromSystem,
		processEnvironment: readCodexAppProcessEnvironmentValue,
	})
}

func inspectSystemCodexAppDaemonReuseWithExpected(
	ctx context.Context,
	expected *codexAppDaemonEnvironment,
) (codexAppDaemonReuseResult, error) {
	return inspectCodexAppDaemonReuseWithDeps(codexAppDaemonInspectDeps{
		hostState:           codexDesktopHostProcessStateFromSystem,
		processEnvironment:  readCodexAppProcessEnvironmentValue,
		launchEnvironment:   codexAppLaunchEnvironment,
		userHome:            os.UserHomeDir,
		expectedEnvironment: expected,
		inspectionContext:   ctx,
	})
}

func inspectCodexAppDaemonReuseWithDeps(deps codexAppDaemonInspectDeps) (codexAppDaemonReuseResult, error) {
	state, err := deps.hostState()
	result := codexAppDaemonReuseResult{
		AppRunning: state.AppRunning, PrivateAppServer: state.PrivateAppServer,
	}
	if err != nil {
		return result, err
	}
	if !state.AppRunning || state.PrivateAppServer {
		return result, nil
	}
	if len(state.AppPIDs) == 0 || deps.processEnvironment == nil {
		return result, fmt.Errorf("running Codex App process identity is incomplete")
	}
	expected := deps.expectedEnvironment
	inspectionContext := deps.inspectionContext
	if inspectionContext == nil {
		inspectionContext = context.Background()
	}
	if expected != nil {
		if validateErr := expected.validate(); validateErr != nil {
			return result, fmt.Errorf("validate running Codex App shared environment: %w", validateErr)
		}
		if deps.launchEnvironment != nil {
			launchEnvironment, resolveErr := codexAppDaemonEnvironmentFromLaunch(
				inspectionContext, deps.launchEnvironment, deps.userHome,
			)
			if resolveErr != nil {
				return result, resolveErr
			}
			if launchEnvironment.CodexHome != expected.CodexHome ||
				launchEnvironment.effectiveSQLiteHome() != expected.effectiveSQLiteHome() {
				return result, fmt.Errorf(
					"%w: launchd Codex environment is not aligned: %s=%s, %s=%s; want %s=%s, effective SQLite home=%s; fully quit and reopen Codex App after correcting launchd environment",
					ErrCodexAppRestartRequired,
					codexAppCodexHomeEnv,
					launchEnvironment.CodexHome,
					codexAppSQLiteHomeEnv,
					launchEnvironment.effectiveSQLiteHome(),
					codexAppCodexHomeEnv,
					expected.CodexHome,
					expected.effectiveSQLiteHome(),
				)
			}
		}
	}
	for _, pid := range state.AppPIDs {
		value, present, envErr := deps.processEnvironment(pid, codexAppUseLocalDaemonEnv)
		if envErr != nil {
			return result, fmt.Errorf("inspect running Codex App daemon environment: %w", envErr)
		}
		if !present || value != "1" {
			return result, fmt.Errorf(
				"%w: running Codex App did not inherit %s=1; fully quit and reopen Codex App",
				ErrCodexAppRestartRequired,
				codexAppUseLocalDaemonEnv,
			)
		}
		if expected != nil {
			if err := inspectCodexAppProcessPathEnvironment(pid, deps.processEnvironment, *expected); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func codexAppDaemonLaunchRestoreArgs(name, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{"unsetenv", name}
	}
	return []string{"setenv", name, value}
}

func rollbackCodexAppDaemonLaunchMutations(
	ctx context.Context,
	launchctl func(context.Context, ...string) (string, error),
	mutations []codexAppDaemonLaunchMutation,
) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexAppLaunchctlTimeout)
	defer cancel()
	var rollbackErrors []error
	for index := len(mutations) - 1; index >= 0; index-- {
		if _, err := launchctl(rollbackCtx, mutations[index].rollback...); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	return errors.Join(rollbackErrors...)
}

func readCodexAppProcessEnvironmentValue(pid int, name string) (string, bool, error) {
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", false, err
	}
	if len(data) > codexHostSnapshotScanLimit {
		return "", false, fmt.Errorf("Codex App 原始进程信息超过 %d 字节", codexHostSnapshotScanLimit)
	}
	return parseDarwinProcessEnvironmentValue(data, name)
}

func inspectCodexAppProcessPathEnvironment(
	pid int,
	processEnvironment func(int, string) (string, bool, error),
	expected codexAppDaemonEnvironment,
) error {
	homeValue, homePresent, err := processEnvironment(pid, codexAppCodexHomeEnv)
	if err != nil {
		return fmt.Errorf("inspect running Codex App %s: %w", codexAppCodexHomeEnv, err)
	}
	actualHome := expected.DefaultCodexHome
	if homePresent && strings.TrimSpace(homeValue) != "" {
		actualHome, err = normalizeCodexAbsoluteEnvironmentPath(codexAppCodexHomeEnv, homeValue)
		if err != nil {
			return fmt.Errorf("inspect running Codex App PID %d: %w", pid, err)
		}
	}
	if actualHome != expected.CodexHome {
		return fmt.Errorf(
			"%w: running Codex App PID %d resolves %s to %s, want %s; fully quit and reopen Codex App",
			ErrCodexAppRestartRequired,
			pid,
			codexAppCodexHomeEnv,
			actualHome,
			expected.CodexHome,
		)
	}

	sqliteValue, sqlitePresent, err := processEnvironment(pid, codexAppSQLiteHomeEnv)
	if err != nil {
		return fmt.Errorf("inspect running Codex App %s: %w", codexAppSQLiteHomeEnv, err)
	}
	actualSQLite := actualHome
	if sqlitePresent && strings.TrimSpace(sqliteValue) != "" {
		actualSQLite, err = normalizeCodexAbsoluteEnvironmentPath(codexAppSQLiteHomeEnv, sqliteValue)
		if err != nil {
			return fmt.Errorf("inspect running Codex App PID %d: %w", pid, err)
		}
	}
	if actualSQLite != expected.effectiveSQLiteHome() {
		return fmt.Errorf(
			"%w: running Codex App PID %d resolves %s to %s, want %s; fully quit and reopen Codex App",
			ErrCodexAppRestartRequired,
			pid,
			codexAppSQLiteHomeEnv,
			actualSQLite,
			expected.effectiveSQLiteHome(),
		)
	}
	return nil
}

func codexAppDaemonEnvironmentFromLaunch(
	ctx context.Context,
	launchEnvironment func(context.Context, string) (string, error),
	userHome func() (string, error),
) (codexAppDaemonEnvironment, error) {
	if launchEnvironment == nil || userHome == nil {
		return codexAppDaemonEnvironment{}, fmt.Errorf("Codex App launchd environment dependencies are incomplete")
	}
	rawHome, err := launchEnvironment(ctx, codexAppCodexHomeEnv)
	if err != nil {
		return codexAppDaemonEnvironment{}, err
	}
	home, err := codexAppHomeFromLaunchValue(rawHome, userHome)
	if err != nil {
		return codexAppDaemonEnvironment{}, err
	}
	userHomePath, err := userHome()
	if err != nil {
		return codexAppDaemonEnvironment{}, fmt.Errorf("resolve Codex App default home: %w", err)
	}
	defaultHome, err := normalizeCodexAbsoluteEnvironmentPath(
		"default CODEX_HOME", filepath.Join(userHomePath, ".codex"),
	)
	if err != nil {
		return codexAppDaemonEnvironment{}, err
	}
	rawSQLite, err := launchEnvironment(ctx, codexAppSQLiteHomeEnv)
	if err != nil {
		return codexAppDaemonEnvironment{}, err
	}
	sqliteHome, err := normalizeOptionalCodexEnvironmentPath(codexAppSQLiteHomeEnv, rawSQLite)
	if err != nil {
		return codexAppDaemonEnvironment{}, err
	}
	environment := codexAppDaemonEnvironment{
		CodexHome:        home,
		CodexSQLiteHome:  sqliteHome,
		DefaultCodexHome: defaultHome,
	}
	if err := environment.validate(); err != nil {
		return codexAppDaemonEnvironment{}, fmt.Errorf("validate launchd Codex environment: %w", err)
	}
	return environment, nil
}

func codexAppHomeFromLaunchValue(
	value string,
	userHome func() (string, error),
) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		home, err := userHome()
		if err != nil {
			return "", fmt.Errorf("resolve Codex App home: %w", err)
		}
		value = filepath.Join(home, ".codex")
	}
	return normalizeCodexAbsoluteEnvironmentPath(codexAppCodexHomeEnv, value)
}

func codexAppDaemonSocketFromLaunchEnvironment(ctx context.Context, deps codexAppDaemonReuseDeps) (string, error) {
	environment, err := codexAppDaemonEnvironmentFromLaunch(
		ctx, deps.launchEnvironment, deps.userHome,
	)
	if err != nil {
		return "", err
	}
	return codexDaemonSocketPath(environment.CodexHome), nil
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
	for pid := range appPIDs {
		state.AppPIDs = append(state.AppPIDs, pid)
	}
	sort.Ints(state.AppPIDs)
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
		// Codex App starts a short-lived private app-server for Code Mode. It
		// carries the features.code_mode_host override and is an MCP/tooling
		// helper, not the App's conversation Host. The main App server may
		// still be connected to the verified official daemon at the same time.
		for _, option := range fields[:index] {
			if option == "features.code_mode_host=true" {
				return false
			}
		}
		return true
	}
	return false
}
