package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type codexDaemonLivePackageManifest struct {
	LayoutVersion int    `json:"layoutVersion"`
	Entrypoint    string `json:"entrypoint"`
	ResourcesDir  string `json:"resourcesDir"`
	PathDir       string `json:"pathDir"`
}

type codexDaemonLiveEntrypointSnapshot struct {
	Command      string
	Exists       bool
	ResolvedPath string
	RealPath     string
	Mode         os.FileMode
	Size         int64
	ModTime      int64
	LinkTarget   string
}

type codexDaemonLiveResidualProcess struct {
	PID  int
	Kind string
}

type codexDaemonLiveUpdaterStopDeps struct {
	readRecord     func(string) (codexDaemonPIDRecord, error)
	inspectProcess func(int) (codexProcessIdentity, error)
	readArgs       func(int) ([]string, error)
	processAlive   func(int) bool
	signalProcess  func(int, syscall.Signal) error
}

type codexDaemonLiveCleanupSteps struct {
	stopDaemon  func() error
	stopUpdater func() error
}

func runCodexDaemonLiveCleanupSteps(steps codexDaemonLiveCleanupSteps) error {
	if steps.stopDaemon == nil || steps.stopUpdater == nil {
		return fmt.Errorf("isolated Codex cleanup steps are incomplete")
	}
	return errors.Join(steps.stopDaemon(), steps.stopUpdater())
}

func copyCodexDaemonLiveStandalonePackage(preparedHome string, runtimeHome string) (err error) {
	sourceRoot, err := resolveCodexDaemonLivePackageRoot(preparedHome)
	if err != nil {
		return err
	}
	destinationRoot := filepath.Join(runtimeHome, "packages", "standalone", "current")
	if _, err := os.Lstat(destinationRoot); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("isolated standalone destination must not exist: path=%s err=%v", destinationRoot, err)
	}
	if err := os.MkdirAll(filepath.Dir(destinationRoot), 0o700); err != nil {
		return fmt.Errorf("create isolated standalone parent: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(destinationRoot)
		}
	}()
	err = filepath.WalkDir(sourceRoot, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil {
			return fmt.Errorf("resolve standalone package entry: %w", err)
		}
		destinationPath := filepath.Join(destinationRoot, relative)
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if err := os.Mkdir(destinationPath, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(destinationPath, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			target, err := validateCodexDaemonLivePackageLink(sourceRoot, sourcePath)
			if err != nil {
				return err
			}
			return os.Symlink(target, destinationPath)
		case info.Mode().IsRegular():
			return copyCodexDaemonLiveRegularFile(sourcePath, destinationPath, info.Mode().Perm())
		default:
			return fmt.Errorf("standalone package contains unsupported special file: %s (%s)", relative, info.Mode())
		}
	})
	if err != nil {
		return fmt.Errorf("copy isolated standalone package: %w", err)
	}
	complete = true
	return nil
}

func copyCodexDaemonLiveRegularFile(source string, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	failed := true
	defer func() {
		_ = output.Close()
		if failed {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Chmod(destination, mode); err != nil {
		return err
	}
	failed = false
	return nil
}

func validateCodexDaemonLivePackageLink(root string, linkPath string) (string, error) {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(target) {
		return "", fmt.Errorf("standalone package link must be relative: %s -> %s", linkPath, target)
	}
	targetPath := filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
	if !codexPathWithinRoot(root, targetPath) {
		return "", fmt.Errorf("standalone package link escapes package root: %s -> %s", linkPath, target)
	}
	realTarget, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		return "", fmt.Errorf("resolve standalone package link %s: %w", linkPath, err)
	}
	if !codexPathWithinRoot(root, realTarget) {
		return "", fmt.Errorf("standalone package link resolves outside package root: %s -> %s", linkPath, target)
	}
	return target, nil
}

func resolveCodexDaemonLivePackageRoot(codexHome string) (string, error) {
	realCodexHome, err := filepath.EvalSymlinks(codexHome)
	if err != nil {
		return "", fmt.Errorf("resolve prepared CODEX_HOME: %w", err)
	}
	current := filepath.Join(codexHome, "packages", "standalone", "current")
	root, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", fmt.Errorf("resolve prepared standalone current package: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve prepared standalone package path: %w", err)
	}
	if !codexPathWithinRoot(realCodexHome, root) {
		return "", fmt.Errorf("prepared standalone current package resolves outside CODEX_HOME: %s", root)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("prepared standalone current package must be a directory: path=%s err=%v", root, err)
	}
	if err := validateCodexDaemonLivePackageManifest(root); err != nil {
		return "", err
	}
	entrypoint := filepath.Join(root, "codex")
	if _, err := validateCodexDaemonLivePackageLinkIfNeeded(root, entrypoint); err != nil {
		return "", err
	}
	if info, err := os.Stat(entrypoint); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("prepared standalone package has no executable codex entrypoint: path=%s err=%v", entrypoint, err)
	}
	return root, nil
}

func validateCodexDaemonLivePackageLinkIfNeeded(root string, path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", nil
	}
	return validateCodexDaemonLivePackageLink(root, path)
}

func validateCodexDaemonLivePackageManifest(root string) error {
	manifestPath := filepath.Join(root, "codex-package.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read prepared standalone package manifest: %w", err)
	}
	var manifest codexDaemonLivePackageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse prepared standalone package manifest: %w", err)
	}
	if manifest.LayoutVersion <= 0 {
		return fmt.Errorf("prepared standalone package manifest has invalid layoutVersion")
	}
	for name, relative := range map[string]string{
		"entrypoint": manifest.Entrypoint,
		"resources":  manifest.ResourcesDir,
		"path":       manifest.PathDir,
	} {
		if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
			return fmt.Errorf("prepared standalone package manifest has invalid %s path %q", name, relative)
		}
		candidate := filepath.Clean(filepath.Join(root, relative))
		if !codexPathWithinRoot(root, candidate) {
			return fmt.Errorf("prepared standalone package manifest %s path escapes package root", name)
		}
		realCandidate, err := filepath.EvalSymlinks(candidate)
		if err != nil || !codexPathWithinRoot(root, realCandidate) {
			return fmt.Errorf("prepared standalone package manifest %s path is invalid: %w", name, err)
		}
	}
	entrypoint := filepath.Join(root, manifest.Entrypoint)
	if info, err := os.Stat(entrypoint); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("prepared standalone manifest entrypoint is not executable: path=%s err=%v", entrypoint, err)
	}
	for _, directory := range []string{manifest.ResourcesDir, manifest.PathDir} {
		if info, err := os.Stat(filepath.Join(root, directory)); err != nil || !info.IsDir() {
			return fmt.Errorf("prepared standalone manifest directory is invalid: path=%s err=%v", directory, err)
		}
	}
	codeModeHost := filepath.Join(root, "bin", "codex-code-mode-host")
	if info, err := os.Stat(codeModeHost); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("prepared standalone package has no executable codex-code-mode-host: path=%s err=%v", codeModeHost, err)
	}
	return nil
}

func validateCodexDaemonLivePreparedHome(
	codexHome string,
	dailyCodexHome string,
	dailyEntrypoint codexDaemonLiveEntrypointSnapshot,
	processes []codexHostProcessSnapshot,
) error {
	if sameCodexDaemonLivePath(codexHome, dailyCodexHome) {
		return fmt.Errorf("prepared CODEX_HOME must not be the current daily Codex home %s", dailyCodexHome)
	}
	if _, err := resolveCodexDaemonLivePackageRoot(codexHome); err != nil {
		return err
	}
	for _, stateRoot := range []string{
		filepath.Join(codexHome, "app-server-daemon"),
		filepath.Join(codexHome, "app-server-control"),
	} {
		if _, err := os.Lstat(stateRoot); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("prepared Codex home contains daemon bootstrap state: path=%s err=%v", stateRoot, err)
		}
	}
	if dailyEntrypoint.Exists &&
		(codexPathWithinRoot(codexHome, dailyEntrypoint.ResolvedPath) ||
			codexPathWithinRoot(codexHome, dailyEntrypoint.RealPath)) {
		return fmt.Errorf("daily Codex command resolves into the prepared test home: %s", dailyEntrypoint.RealPath)
	}
	if residuals := findCodexDaemonLiveResidualProcesses(codexHome, processes); len(residuals) > 0 {
		return fmt.Errorf("prepared Codex home still owns runtime processes: %s", formatCodexDaemonLiveResidualProcesses(residuals))
	}
	return nil
}

func codexDaemonLiveEnvironment(userHome string, codexHome string, sqliteHome string, packageRoot string) map[string]string {
	pathEntries := []string{
		filepath.Join(userHome, ".local", "bin"),
		filepath.Join(packageRoot, "codex-path"),
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}
	return map[string]string{
		"HOME":              userHome,
		"CODEX_HOME":        codexHome,
		"CODEX_SQLITE_HOME": sqliteHome,
		"PATH":              strings.Join(pathEntries, string(os.PathListSeparator)),
		"XDG_CACHE_HOME":    filepath.Join(userHome, ".cache"),
		"XDG_CONFIG_HOME":   filepath.Join(userHome, ".config"),
		"XDG_DATA_HOME":     filepath.Join(userHome, ".local", "share"),
		"XDG_STATE_HOME":    filepath.Join(userHome, ".local", "state"),
	}
}

func createCodexDaemonLiveRuntimeRoot() (string, error) {
	tempRoot := canonicalCodexPath(filepath.Join(string(filepath.Separator), "tmp"))
	info, err := os.Stat(tempRoot)
	if err != nil {
		return "", fmt.Errorf("resolve short system temp root %s: %w", tempRoot, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("short system temp root is not a directory: %s", tempRoot)
	}
	runtimeRoot, err := os.MkdirTemp(tempRoot, "weclaw-codex-live.")
	if err != nil {
		return "", fmt.Errorf("create isolated Codex runtime home: %w", err)
	}
	socketPath := codexDaemonSocketPath(filepath.Join(runtimeRoot, "codex-home"))
	if len([]byte(socketPath)) > codexHostSocketMaxBytes {
		_ = os.RemoveAll(runtimeRoot)
		return "", fmt.Errorf(
			"isolated Codex daemon socket path is too long (%d bytes, max %d): %s",
			len([]byte(socketPath)), codexHostSocketMaxBytes, socketPath,
		)
	}
	return runtimeRoot, nil
}

func snapshotCodexDaemonLiveEntrypoint(command string) (codexDaemonLiveEntrypointSnapshot, error) {
	snapshot := codexDaemonLiveEntrypointSnapshot{Command: command}
	resolved, err := exec.LookPath(command)
	if errors.Is(err, exec.ErrNotFound) {
		return snapshot, nil
	}
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return snapshot, fmt.Errorf("resolve daily Codex command %q: %w", command, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return snapshot, fmt.Errorf("resolve daily Codex command path: %w", err)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return snapshot, fmt.Errorf("inspect daily Codex command %s: %w", resolved, err)
	}
	realPath, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return snapshot, fmt.Errorf("resolve daily Codex command symlink %s: %w", resolved, err)
	}
	snapshot.Exists = true
	snapshot.ResolvedPath = filepath.Clean(resolved)
	snapshot.RealPath = filepath.Clean(realPath)
	snapshot.Mode = info.Mode()
	snapshot.Size = info.Size()
	snapshot.ModTime = info.ModTime().UnixNano()
	if info.Mode()&os.ModeSymlink != 0 {
		snapshot.LinkTarget, err = os.Readlink(resolved)
		if err != nil {
			return codexDaemonLiveEntrypointSnapshot{}, fmt.Errorf("read daily Codex command symlink: %w", err)
		}
	}
	return snapshot, nil
}

func snapshotCodexDaemonLiveEntrypoints() ([]codexDaemonLiveEntrypointSnapshot, error) {
	entrypoint, err := snapshotCodexDaemonLiveEntrypoint("codex")
	if err != nil {
		return nil, err
	}
	entrypoints := []codexDaemonLiveEntrypointSnapshot{entrypoint}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve daily user home for Codex entrypoint: %w", err)
	}
	userLocalEntrypoint := filepath.Join(userHome, ".local", "bin", "codex")
	if filepath.Clean(entrypoint.ResolvedPath) == filepath.Clean(userLocalEntrypoint) {
		return entrypoints, nil
	}
	localSnapshot, err := snapshotCodexDaemonLiveEntrypoint(userLocalEntrypoint)
	if err != nil {
		return nil, err
	}
	return append(entrypoints, localSnapshot), nil
}

func compareCodexDaemonLiveEntrypoints(before codexDaemonLiveEntrypointSnapshot, after codexDaemonLiveEntrypointSnapshot) error {
	if before != after {
		return fmt.Errorf(
			"daily Codex command changed during the live gate: before=%s after=%s",
			formatCodexDaemonLiveEntrypoint(before),
			formatCodexDaemonLiveEntrypoint(after),
		)
	}
	return nil
}

func formatCodexDaemonLiveEntrypoint(snapshot codexDaemonLiveEntrypointSnapshot) string {
	if !snapshot.Exists {
		return "absent"
	}
	return fmt.Sprintf("path=%s real=%s mode=%s size=%d mtime=%d link=%q",
		snapshot.ResolvedPath, snapshot.RealPath, snapshot.Mode, snapshot.Size, snapshot.ModTime, snapshot.LinkTarget)
}

func findCodexDaemonLiveResidualProcesses(root string, processes []codexHostProcessSnapshot) []codexDaemonLiveResidualProcess {
	residuals := make([]codexDaemonLiveResidualProcess, 0)
	for _, process := range processes {
		if !codexDaemonLiveProcessWithinRoot(root, process) {
			continue
		}
		kind := "codex-process"
		joinedArgs := strings.ToLower(strings.Join(process.Args, "\x00"))
		command := strings.ToLower(process.Command)
		switch {
		case strings.Contains(joinedArgs, "app-server\x00daemon\x00pid-update-loop") ||
			strings.Contains(command, "app-server daemon pid-update-loop"):
			kind = "updater"
		case strings.Contains(joinedArgs, "codex-code-mode-host") ||
			strings.Contains(command, "codex-code-mode-host"):
			kind = "code-mode-host"
		case codexAppServerHostProcess(process.Executable, process.Command, process.Args):
			kind = "app-server"
		}
		residuals = append(residuals, codexDaemonLiveResidualProcess{PID: process.PID, Kind: kind})
	}
	sort.Slice(residuals, func(i, j int) bool { return residuals[i].PID < residuals[j].PID })
	return residuals
}

func codexDaemonLiveProcessWithinRoot(root string, process codexHostProcessSnapshot) bool {
	for _, argument := range process.Args {
		if filepath.IsAbs(argument) && codexPathWithinRoot(root, argument) {
			return true
		}
	}
	if filepath.IsAbs(process.Executable) && codexPathWithinRoot(root, process.Executable) {
		return true
	}
	command := strings.TrimSpace(process.Command)
	for _, prefix := range []string{root + string(filepath.Separator), `"` + root + string(filepath.Separator), `'` + root + string(filepath.Separator)} {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

func formatCodexDaemonLiveResidualProcesses(processes []codexDaemonLiveResidualProcess) string {
	parts := make([]string, 0, len(processes))
	for _, process := range processes {
		parts = append(parts, fmt.Sprintf("%s PID %d", process.Kind, process.PID))
	}
	return strings.Join(parts, ", ")
}

func snapshotCodexDaemonLiveProcesses(ctx context.Context) ([]codexHostProcessSnapshot, error) {
	allowedUIDs := map[uint32]struct{}{uint32(os.Geteuid()): {}}
	return systemCodexHostProcessSnapshot(ctx, allowedUIDs)
}

func verifyCodexDaemonLiveCleanup(runtime *codexDaemonLiveRuntimeHome) error {
	return verifyCodexDaemonLiveCleanupWithDeps(
		runtime,
		snapshotCodexDaemonLiveProcesses,
		snapshotCodexDaemonLiveEntrypoint,
		5*time.Second,
	)
}

func verifyCodexDaemonLiveCleanupWithDeps(
	runtime *codexDaemonLiveRuntimeHome,
	processSnapshot func(context.Context) ([]codexHostProcessSnapshot, error),
	entrypointSnapshot func(string) (codexDaemonLiveEntrypointSnapshot, error),
	wait time.Duration,
) error {
	if processSnapshot == nil || entrypointSnapshot == nil {
		return fmt.Errorf("isolated Codex cleanup dependencies are incomplete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if wait > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), wait)
	}
	defer cancel()
	var residuals []codexDaemonLiveResidualProcess
	var processErr error
	for {
		processes, err := processSnapshot(ctx)
		if err != nil {
			processErr = fmt.Errorf("inspect isolated Codex processes after cleanup: %w", err)
			break
		}
		residuals = findCodexDaemonLiveResidualProcesses(runtime.path, processes)
		if len(residuals) == 0 {
			break
		}
		if wait <= 0 {
			processErr = fmt.Errorf("isolated Codex processes remain after daemon stop: %s",
				formatCodexDaemonLiveResidualProcesses(residuals))
			break
		}
		if err := ctx.Err(); err != nil {
			processErr = fmt.Errorf("isolated Codex processes remain after daemon stop: %s: %w",
				formatCodexDaemonLiveResidualProcesses(residuals), err)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	entrypointErrors := make([]error, 0, len(runtime.dailyEntrypoints))
	for _, before := range runtime.dailyEntrypoints {
		after, err := entrypointSnapshot(before.Command)
		if err != nil {
			entrypointErrors = append(entrypointErrors,
				fmt.Errorf("inspect daily Codex command %s after live gate: %w", before.Command, err))
			continue
		}
		entrypointErrors = append(entrypointErrors, compareCodexDaemonLiveEntrypoints(before, after))
	}
	return errors.Join(processErr, errors.Join(entrypointErrors...))
}

func stopCodexDaemonLiveUpdater(ctx context.Context, agent *ACPAgent, runtime *codexDaemonLiveRuntimeHome) error {
	if agent == nil || runtime == nil {
		return fmt.Errorf("isolated Codex updater cleanup runtime is unavailable")
	}
	return stopCodexDaemonLiveUpdaterWithDeps(ctx, runtime.codexHome, codexDaemonLiveUpdaterStopDeps{
		readRecord:     agent.readCodexDaemonPIDRecord,
		inspectProcess: inspectCodexHostProcess,
		readArgs:       readCodexHostProcessArgs,
		processAlive:   codexHostProcessAlive,
		signalProcess: func(pid int, signal syscall.Signal) error {
			return syscall.Kill(pid, signal)
		},
	})
}

func stopCodexDaemonLiveUpdaterWithDeps(
	ctx context.Context,
	codexHome string,
	deps codexDaemonLiveUpdaterStopDeps,
) error {
	if !isCodexLiveTestTemporaryPath(codexHome) {
		return fmt.Errorf("refuse to stop updater outside a WeClaw live-test runtime: %s", codexHome)
	}
	if deps.readRecord == nil || deps.inspectProcess == nil || deps.readArgs == nil ||
		deps.processAlive == nil || deps.signalProcess == nil {
		return fmt.Errorf("isolated Codex updater cleanup dependencies are incomplete")
	}
	recordPath := filepath.Join(codexHome, "app-server-daemon", "app-server-updater.pid")
	record, err := deps.readRecord(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read isolated Codex updater identity: %w", err)
	}
	if !deps.processAlive(record.PID) {
		return nil
	}
	identity, err := deps.inspectProcess(record.PID)
	if err != nil {
		if !deps.processAlive(record.PID) {
			return nil
		}
		return fmt.Errorf("inspect isolated Codex updater PID %d: %w", record.PID, err)
	}
	args, err := deps.readArgs(record.PID)
	if err != nil {
		if !deps.processAlive(record.PID) {
			return nil
		}
		return fmt.Errorf("read isolated Codex updater PID %d argv: %w", record.PID, err)
	}
	expectedCommand := codexDaemonManagedBinaryPath(codexHome)
	if err := validateCodexDaemonLiveUpdaterIdentity(record, identity, args, expectedCommand); err != nil {
		return err
	}
	if err := deps.signalProcess(record.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop verified isolated Codex updater PID %d: %w", record.PID, err)
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for deps.processAlive(record.PID) {
		current, err := deps.inspectProcess(record.PID)
		if err != nil {
			if !deps.processAlive(record.PID) {
				return nil
			}
			return fmt.Errorf("verify isolated Codex updater PID %d exit: %w", record.PID, err)
		}
		if current.start != record.ProcessStartTime {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for isolated Codex updater PID %d exit: %w", record.PID, ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

func validateCodexDaemonLiveUpdaterIdentity(
	record codexDaemonPIDRecord,
	identity codexProcessIdentity,
	args []string,
	expectedBinary string,
) error {
	if identity.uid != uint32(os.Geteuid()) || identity.pgid != record.PID ||
		identity.start != record.ProcessStartTime {
		return fmt.Errorf("isolated Codex updater PID %d identity mismatch; refusing to signal", record.PID)
	}
	if !codexDaemonLiveUpdaterArgsMatch(args, expectedBinary) {
		return fmt.Errorf("isolated Codex updater PID %d command mismatch; refusing to signal", record.PID)
	}
	return nil
}

func codexDaemonLiveUpdaterArgsMatch(args []string, expectedBinary string) bool {
	const updaterArgCount = 4
	if len(args) == updaterArgCount && codexDaemonLiveSameExecutable(args[0], expectedBinary) {
		return args[1] == "app-server" && args[2] == "daemon" && args[3] == "pid-update-loop"
	}
	if len(args) != updaterArgCount+1 || strings.ToLower(filepath.Base(args[0])) != "node" ||
		!codexDaemonLiveSameExecutable(args[1], expectedBinary) {
		return false
	}
	return args[2] == "app-server" && args[3] == "daemon" && args[4] == "pid-update-loop"
}

func codexDaemonLiveSameExecutable(candidate string, expected string) bool {
	candidate = filepath.Clean(strings.TrimSpace(candidate))
	expected = filepath.Clean(strings.TrimSpace(expected))
	if candidate == expected {
		return true
	}
	candidateInfo, candidateErr := os.Stat(candidate)
	expectedInfo, expectedErr := os.Stat(expected)
	return candidateErr == nil && expectedErr == nil && os.SameFile(candidateInfo, expectedInfo)
}
