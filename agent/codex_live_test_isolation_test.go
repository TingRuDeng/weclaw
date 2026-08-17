package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestACPStartRejectsCodexLiveTestPathsBeforeSideEffects(t *testing.T) {
	safeRoot := t.TempDir()
	safeCommand := filepath.Join(safeRoot, "codex")
	writeCodexDaemonLiveTestFile(t, safeCommand, "safe", 0o755)
	safeHome := filepath.Join(safeRoot, "codex-home")
	if err := os.Mkdir(safeHome, 0o700); err != nil {
		t.Fatal(err)
	}

	liveRoot := filepath.Join(t.TempDir(), "weclaw-codex-live.fixture")
	liveCommand := filepath.Join(liveRoot, "packages", "standalone", "current", "codex")
	writeCodexDaemonLiveTestFile(t, liveCommand, "live", 0o755)
	linkedCommand := filepath.Join(safeRoot, "linked", "codex")
	if err := os.MkdirAll(filepath.Dir(linkedCommand), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(liveCommand, linkedCommand); err != nil {
		t.Fatal(err)
	}
	linkedHome := filepath.Join(safeRoot, "linked-codex-home")
	if err := os.Symlink(liveRoot, linkedHome); err != nil {
		t.Fatal(err)
	}
	relativePathAlias := filepath.Join(safeRoot, "live-bin-alias")
	if err := os.Symlink(filepath.Dir(liveCommand), relativePathAlias); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		command string
		home    string
		path    string
	}{
		{name: "direct command", command: liveCommand, home: safeHome},
		{name: "resolved command symlink", command: linkedCommand, home: safeHome},
		{name: "agent PATH command", command: "codex", home: safeHome, path: filepath.Dir(liveCommand)},
		{name: "relative agent PATH symlink", command: "codex", home: safeHome, path: filepath.Base(relativePathAlias)},
		{name: "CODEX_HOME", command: safeCommand, home: filepath.Join(liveRoot, "runtime-home")},
		{name: "resolved CODEX_HOME symlink", command: safeCommand, home: linkedHome},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enabled := true
			env := map[string]string{"CODEX_HOME": test.home}
			if test.path != "" {
				env["PATH"] = test.path
			}
			a := NewACPAgent(ACPAgentConfig{
				Command:            test.command,
				Args:               []string{"app-server"},
				Cwd:                safeRoot,
				Env:                env,
				StateFile:          filepath.Join(t.TempDir(), "state.json"),
				CodexHostMode:      codexHostModeDaemon,
				CodexAppDaemon:     &enabled,
				CodexDesktopBridge: true,
			})
			var sideEffectCalled bool
			a.codexAppDaemonReuseCall = func(context.Context, bool, string) (codexAppDaemonReuseResult, error) {
				sideEffectCalled = true
				return codexAppDaemonReuseResult{}, nil
			}

			err := a.Start(context.Background())
			if !errors.Is(err, errCodexLiveTestPathLeak) {
				t.Fatalf("Start() error=%v, want live-test path rejection", err)
			}
			if sideEffectCalled {
				t.Fatal("Codex App reuse must not run before the live-test path rejection")
			}
		})
	}
}

func TestCodexLiveTestPathPermissionIsPackageInternal(t *testing.T) {
	liveCommand := filepath.Join(t.TempDir(), "weclaw-codex-live.fixture", "codex")
	writeCodexDaemonLiveTestFile(t, liveCommand, "live", 0o755)
	a := newACPAgent(ACPAgentConfig{
		Command: liveCommand,
		Args:    []string{"app-server"},
		Cwd:     t.TempDir(),
		Env:     map[string]string{"CODEX_HOME": filepath.Dir(liveCommand)},
	}, acpAgentOptions{allowCodexLiveTestPaths: true})
	if err := a.validateCodexLiveTestPathIsolation(); err != nil {
		t.Fatalf("internal live gate permission error=%v", err)
	}
}

func TestIsCodexLiveTestTemporaryPathRecognizesReservedPrefix(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/tmp/weclaw-codex-live.fixture/codex", want: true},
		{path: "/tmp/weclaw-codex-live-runtime-fixture/codex", want: true},
		{path: "/tmp/weclaw-codex-live-old/codex", want: true},
		{path: "/tmp/prefix-weclaw-codex-live.fixture/codex", want: false},
		{path: "/tmp/weclaw-codex/codex", want: false},
	}
	for _, test := range tests {
		if got := isCodexLiveTestTemporaryPath(test.path); got != test.want {
			t.Errorf("isCodexLiveTestTemporaryPath(%q)=%v, want %v", test.path, got, test.want)
		}
	}
}

func TestCopyCodexDaemonLiveStandalonePackageCopiesCompleteIndependentTree(t *testing.T) {
	preparedHome, sourcePackage := writeCodexDaemonLiveTestPackage(t)
	runtimeHome := t.TempDir()

	if err := copyCodexDaemonLiveStandalonePackage(preparedHome, runtimeHome); err != nil {
		t.Fatalf("copyCodexDaemonLiveStandalonePackage() error=%v", err)
	}
	destination := filepath.Join(runtimeHome, "packages", "standalone", "current")
	for _, relativePath := range []string{
		"bin/codex",
		"bin/codex-code-mode-host",
		"codex-path/rg",
		"codex-resources/zsh/bin/zsh",
		"codex-package.json",
	} {
		if info, err := os.Stat(filepath.Join(destination, relativePath)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("copied package path %s: info=%v err=%v", relativePath, info, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(destination, "codex")); err != nil || target != filepath.Join("bin", "codex") {
		t.Fatalf("copied codex link target=%q err=%v", target, err)
	}

	destinationBinary := filepath.Join(destination, "bin", "codex")
	if err := os.WriteFile(destinationBinary, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceData, err := os.ReadFile(filepath.Join(sourcePackage, "bin", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceData) != "codex" {
		t.Fatalf("source package changed through runtime copy: %q", sourceData)
	}
}

func TestCopyCodexDaemonLiveStandalonePackageRejectsUnsafeLinks(t *testing.T) {
	tests := []struct {
		name   string
		target func(t *testing.T, packageRoot string) string
	}{
		{
			name: "absolute",
			target: func(t *testing.T, packageRoot string) string {
				outside := filepath.Join(t.TempDir(), "codex")
				writeCodexDaemonLiveTestFile(t, outside, "outside", 0o755)
				return outside
			},
		},
		{
			name: "outside package",
			target: func(t *testing.T, packageRoot string) string {
				outside := filepath.Join(filepath.Dir(packageRoot), "outside-codex")
				writeCodexDaemonLiveTestFile(t, outside, "outside", 0o755)
				relative, err := filepath.Rel(packageRoot, outside)
				if err != nil {
					t.Fatal(err)
				}
				return relative
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preparedHome, packageRoot := writeCodexDaemonLiveTestPackage(t)
			entrypoint := filepath.Join(packageRoot, "codex")
			if err := os.Remove(entrypoint); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(test.target(t, packageRoot), entrypoint); err != nil {
				t.Fatal(err)
			}
			if err := copyCodexDaemonLiveStandalonePackage(preparedHome, t.TempDir()); err == nil {
				t.Fatal("copyCodexDaemonLiveStandalonePackage() error=nil, want unsafe link rejection")
			}
		})
	}
}

func TestValidateCodexDaemonLivePreparedHomeRejectsBootstrapStateAndProcesses(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, home string, packageRoot string) (codexDaemonLiveEntrypointSnapshot, []codexHostProcessSnapshot)
	}{
		{
			name: "daemon lock",
			prepare: func(t *testing.T, home string, _ string) (codexDaemonLiveEntrypointSnapshot, []codexHostProcessSnapshot) {
				writeCodexDaemonLiveTestFile(t, filepath.Join(home, "app-server-daemon", "daemon.lock"), "lock", 0o600)
				return codexDaemonLiveEntrypointSnapshot{}, nil
			},
		},
		{
			name: "updater pid lock",
			prepare: func(t *testing.T, home string, _ string) (codexDaemonLiveEntrypointSnapshot, []codexHostProcessSnapshot) {
				writeCodexDaemonLiveTestFile(t, filepath.Join(home, "app-server-daemon", "app-server-updater.pid.lock"), "lock", 0o600)
				return codexDaemonLiveEntrypointSnapshot{}, nil
			},
		},
		{
			name: "control state directory",
			prepare: func(t *testing.T, home string, _ string) (codexDaemonLiveEntrypointSnapshot, []codexHostProcessSnapshot) {
				if err := os.MkdirAll(filepath.Join(home, "app-server-control"), 0o700); err != nil {
					t.Fatal(err)
				}
				return codexDaemonLiveEntrypointSnapshot{}, nil
			},
		},
		{
			name: "daily command points into prepared home",
			prepare: func(_ *testing.T, _ string, packageRoot string) (codexDaemonLiveEntrypointSnapshot, []codexHostProcessSnapshot) {
				return codexDaemonLiveEntrypointSnapshot{
					Command: "codex", Exists: true,
					ResolvedPath: "/safe/bin/codex",
					RealPath:     filepath.Join(packageRoot, "bin", "codex"),
				}, nil
			},
		},
		{
			name: "detached updater",
			prepare: func(_ *testing.T, _ string, packageRoot string) (codexDaemonLiveEntrypointSnapshot, []codexHostProcessSnapshot) {
				binary := filepath.Join(packageRoot, "codex")
				return codexDaemonLiveEntrypointSnapshot{}, []codexHostProcessSnapshot{{
					PID: 101, UID: 501, Executable: binary,
					Args: []string{binary, "app-server", "daemon", "pid-update-loop"},
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home, packageRoot := writeCodexDaemonLiveTestPackage(t)
			entry, processes := test.prepare(t, home, packageRoot)
			if err := validateCodexDaemonLivePreparedHome(home, t.TempDir(), entry, processes); err == nil {
				t.Fatal("validateCodexDaemonLivePreparedHome() error=nil, want polluted home rejection")
			}
		})
	}
}

func TestValidateCodexDaemonLivePreparedHomeAcceptsCleanPackage(t *testing.T) {
	home, _ := writeCodexDaemonLiveTestPackage(t)
	if err := validateCodexDaemonLivePreparedHome(
		home, t.TempDir(), codexDaemonLiveEntrypointSnapshot{}, nil,
	); err != nil {
		t.Fatalf("validateCodexDaemonLivePreparedHome() error=%v", err)
	}
}

func TestValidateCodexDaemonLivePreparedHomeRejectsCurrentOutsideHome(t *testing.T) {
	home, _ := writeCodexDaemonLiveTestPackage(t)
	_, outsidePackage := writeCodexDaemonLiveTestPackage(t)
	current := filepath.Join(home, "packages", "standalone", "current")
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePackage, current); err != nil {
		t.Fatal(err)
	}
	if err := validateCodexDaemonLivePreparedHome(
		home, t.TempDir(), codexDaemonLiveEntrypointSnapshot{}, nil,
	); err == nil {
		t.Fatal("validateCodexDaemonLivePreparedHome() error=nil, want external current rejection")
	}
}

func TestCodexDaemonLiveEnvironmentIsolatesUserPaths(t *testing.T) {
	runtimeRoot := t.TempDir()
	dailyHome := filepath.Join(runtimeRoot, "daily-home")
	t.Setenv("HOME", dailyHome)
	userHome := filepath.Join(runtimeRoot, "user-home")
	codexHome := filepath.Join(runtimeRoot, "codex-home")
	sqliteHome := filepath.Join(runtimeRoot, "sqlite-home")
	packageRoot := filepath.Join(codexHome, "packages", "standalone", "current")
	env := codexDaemonLiveEnvironment(userHome, codexHome, sqliteHome, packageRoot)

	if env["HOME"] != userHome || env["CODEX_HOME"] != codexHome || env["CODEX_SQLITE_HOME"] != sqliteHome {
		t.Fatalf("isolated env=%#v", env)
	}
	pathEntries := filepath.SplitList(env["PATH"])
	if len(pathEntries) < 2 || pathEntries[0] != filepath.Join(userHome, ".local", "bin") ||
		pathEntries[1] != filepath.Join(packageRoot, "codex-path") {
		t.Fatalf("isolated PATH=%q", env["PATH"])
	}
	for _, entry := range pathEntries {
		if strings.HasPrefix(entry, dailyHome+string(filepath.Separator)) {
			t.Fatalf("isolated PATH leaked daily home entry %q", entry)
		}
	}
}

func TestPrepareCodexDaemonLiveRuntimeHomeUsesShortCanonicalTempRoot(t *testing.T) {
	longTempRoot := filepath.Join(t.TempDir(), strings.Repeat("long-temp-root-", 6))
	if err := os.MkdirAll(longTempRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", longTempRoot)
	runtimeRoot, err := createCodexDaemonLiveRuntimeRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	socketPath := codexDaemonSocketPath(filepath.Join(runtimeRoot, "codex-home"))
	if got := len([]byte(socketPath)); got > codexHostSocketMaxBytes {
		t.Fatalf("isolated daemon socket path is %d bytes, max %d: %s", got, codexHostSocketMaxBytes, socketPath)
	}
	realRuntime, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeRoot != realRuntime {
		t.Fatalf("runtime path=%q, want canonical path %q", runtimeRoot, realRuntime)
	}
}

func TestCodexPathWithinRootRecognizesCanonicalSymlinkAlias(t *testing.T) {
	realRoot := t.TempDir()
	candidate := filepath.Join(realRoot, "bin", "codex")
	writeCodexDaemonLiveTestFile(t, candidate, "codex", 0o755)
	aliasRoot := filepath.Join(t.TempDir(), "runtime-alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Fatal(err)
	}

	if !codexPathWithinRoot(aliasRoot, candidate) {
		t.Fatalf("candidate %q should be recognized within canonical root alias %q", candidate, aliasRoot)
	}
}

func TestFindCodexDaemonLiveResidualProcessesIncludesUpdaterAndCodeModeHost(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "weclaw-codex-live-runtime-fixture")
	binary := filepath.Join(runtimeRoot, "packages", "standalone", "current", "codex")
	codeMode := filepath.Join(runtimeRoot, "packages", "standalone", "current", "bin", "codex-code-mode-host")
	processes := []codexHostProcessSnapshot{
		{PID: 101, Executable: binary, Args: []string{binary, "app-server", "--listen", "unix:///tmp/live.sock"}},
		{PID: 102, Executable: binary, Args: []string{binary, "app-server", "daemon", "pid-update-loop"}},
		{PID: 103, Executable: "codex-code-mode-", Command: codeMode + " --listen stdio://"},
		{PID: 201, Executable: "/Applications/Codex.app/codex", Args: []string{"/Applications/Codex.app/codex", "app-server"}},
	}

	residuals := findCodexDaemonLiveResidualProcesses(runtimeRoot, processes)
	if len(residuals) != 3 {
		t.Fatalf("residuals=%#v, want 3 isolated processes", residuals)
	}
	wantKinds := map[string]bool{"app-server": true, "updater": true, "code-mode-host": true}
	for _, residual := range residuals {
		if !wantKinds[residual.Kind] {
			t.Fatalf("unexpected residual=%#v", residual)
		}
		delete(wantKinds, residual.Kind)
	}
	if len(wantKinds) != 0 {
		t.Fatalf("missing residual kinds=%v", wantKinds)
	}
}

func TestFindCodexDaemonLiveResidualProcessesRecognizesRuntimeBinaryBehindWrapper(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "weclaw-codex-live-runtime-fixture")
	binary := filepath.Join(runtimeRoot, "packages", "standalone", "current", "codex")
	processes := []codexHostProcessSnapshot{{
		PID:        104,
		Executable: "/usr/bin/node",
		Args:       []string{"/usr/bin/node", binary, "app-server", "daemon", "pid-update-loop"},
	}}

	residuals := findCodexDaemonLiveResidualProcesses(runtimeRoot, processes)
	if len(residuals) != 1 || residuals[0].PID != 104 || residuals[0].Kind != "updater" {
		t.Fatalf("residuals=%#v, want wrapped updater PID 104", residuals)
	}
}

func TestCompareCodexDaemonLiveEntrypointsDetectsMutation(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "bin")
	firstTarget := filepath.Join(t.TempDir(), "codex-first")
	secondTarget := filepath.Join(t.TempDir(), "codex-second")
	writeCodexDaemonLiveTestFile(t, firstTarget, "first", 0o755)
	writeCodexDaemonLiveTestFile(t, secondTarget, "second", 0o755)
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	entrypoint := filepath.Join(binDir, "codex")
	if err := os.Symlink(firstTarget, entrypoint); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	before, err := snapshotCodexDaemonLiveEntrypoint("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := compareCodexDaemonLiveEntrypoints(before, before); err != nil {
		t.Fatalf("unchanged entrypoint error=%v", err)
	}
	if err := os.Remove(entrypoint); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondTarget, entrypoint); err != nil {
		t.Fatal(err)
	}
	after, err := snapshotCodexDaemonLiveEntrypoint("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := compareCodexDaemonLiveEntrypoints(before, after); err == nil {
		t.Fatal("compareCodexDaemonLiveEntrypoints() error=nil, want mutation rejection")
	}
}

func TestSnapshotCodexDaemonLiveEntrypointsIncludesUserLocalAliasOutsidePATH(t *testing.T) {
	userHome := t.TempDir()
	target := filepath.Join(t.TempDir(), "codex")
	writeCodexDaemonLiveTestFile(t, target, "codex", 0o755)
	alias := filepath.Join(userHome, ".local", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(alias), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	t.Setenv("PATH", t.TempDir())

	entrypoints, err := snapshotCodexDaemonLiveEntrypoints()
	if err != nil {
		t.Fatal(err)
	}
	expectedRealPath, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, entrypoint := range entrypoints {
		if entrypoint.Command == alias && entrypoint.Exists && entrypoint.RealPath == expectedRealPath {
			return
		}
	}
	t.Fatalf("entrypoints=%#v, want user-local alias %s", entrypoints, alias)
}

func TestVerifyCodexDaemonLiveCleanupFailsClosed(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "weclaw-codex-live-runtime-fixture")
	binary := filepath.Join(runtimeRoot, "codex-home", "packages", "standalone", "current", "codex")
	runtime := &codexDaemonLiveRuntimeHome{
		path: runtimeRoot,
		dailyEntrypoints: []codexDaemonLiveEntrypointSnapshot{{
			Command: "codex",
		}},
	}
	unchangedEntrypoint := func(string) (codexDaemonLiveEntrypointSnapshot, error) {
		return runtime.dailyEntrypoints[0], nil
	}

	t.Run("residual updater", func(t *testing.T) {
		err := verifyCodexDaemonLiveCleanupWithDeps(
			runtime,
			func(context.Context) ([]codexHostProcessSnapshot, error) {
				return []codexHostProcessSnapshot{{
					PID: 101, Executable: binary,
					Args: []string{binary, "app-server", "daemon", "pid-update-loop"},
				}}, nil
			},
			unchangedEntrypoint,
			0,
		)
		if err == nil || !strings.Contains(err.Error(), "updater") {
			t.Fatalf("verify cleanup error=%v, want updater evidence", err)
		}
	})

	t.Run("process snapshot error", func(t *testing.T) {
		wantErr := errors.New("snapshot denied")
		err := verifyCodexDaemonLiveCleanupWithDeps(
			runtime,
			func(context.Context) ([]codexHostProcessSnapshot, error) { return nil, wantErr },
			unchangedEntrypoint,
			0,
		)
		if !errors.Is(err, wantErr) {
			t.Fatalf("verify cleanup error=%v, want snapshot cause", err)
		}
	})

	t.Run("daily entrypoint mutation", func(t *testing.T) {
		changed := codexDaemonLiveEntrypointSnapshot{Command: "codex", Exists: true, ResolvedPath: "/safe/codex"}
		err := verifyCodexDaemonLiveCleanupWithDeps(
			runtime,
			func(context.Context) ([]codexHostProcessSnapshot, error) { return nil, nil },
			func(string) (codexDaemonLiveEntrypointSnapshot, error) { return changed, nil },
			0,
		)
		if err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("verify cleanup error=%v, want entrypoint mutation", err)
		}
	})

	t.Run("reports residual and entrypoint mutation together", func(t *testing.T) {
		changed := codexDaemonLiveEntrypointSnapshot{Command: "codex", Exists: true, ResolvedPath: "/safe/codex"}
		err := verifyCodexDaemonLiveCleanupWithDeps(
			runtime,
			func(context.Context) ([]codexHostProcessSnapshot, error) {
				return []codexHostProcessSnapshot{{
					PID: 101, Executable: binary,
					Args: []string{binary, "app-server", "daemon", "pid-update-loop"},
				}}, nil
			},
			func(string) (codexDaemonLiveEntrypointSnapshot, error) { return changed, nil },
			0,
		)
		if err == nil || !strings.Contains(err.Error(), "updater") || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("verify cleanup error=%v, want both residual and entrypoint evidence", err)
		}
	})
}

func TestStopCodexDaemonLiveUpdaterSignalsOnlyVerifiedProcess(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "weclaw-codex-live-runtime-fixture", "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	managedBinary := codexDaemonManagedBinaryPath(codexHome)
	record := codexDaemonPIDRecord{PID: 101, ProcessStartTime: "verified-start"}
	verifiedIdentity := codexProcessIdentity{
		uid: uint32(os.Geteuid()), pgid: record.PID, start: record.ProcessStartTime,
	}
	verifiedArgs := []string{managedBinary, "app-server", "daemon", "pid-update-loop"}

	t.Run("verified updater", func(t *testing.T) {
		alive := true
		var signaled bool
		deps := codexDaemonLiveUpdaterStopDeps{
			readRecord: func(string) (codexDaemonPIDRecord, error) { return record, nil },
			inspectProcess: func(int) (codexProcessIdentity, error) {
				return verifiedIdentity, nil
			},
			readArgs:     func(int) ([]string, error) { return verifiedArgs, nil },
			processAlive: func(int) bool { return alive },
			signalProcess: func(pid int, signal syscall.Signal) error {
				if pid != record.PID || signal != syscall.SIGTERM {
					t.Fatalf("signal pid=%d signal=%v", pid, signal)
				}
				signaled = true
				alive = false
				return nil
			},
		}
		if err := stopCodexDaemonLiveUpdaterWithDeps(context.Background(), codexHome, deps); err != nil {
			t.Fatalf("stop verified updater error=%v", err)
		}
		if !signaled {
			t.Fatal("verified updater was not signaled")
		}
	})

	t.Run("verified updater behind node wrapper", func(t *testing.T) {
		alive := true
		var signaled bool
		deps := codexDaemonLiveUpdaterStopDeps{
			readRecord:     func(string) (codexDaemonPIDRecord, error) { return record, nil },
			inspectProcess: func(int) (codexProcessIdentity, error) { return verifiedIdentity, nil },
			readArgs: func(int) ([]string, error) {
				return []string{"/usr/bin/node", managedBinary, "app-server", "daemon", "pid-update-loop"}, nil
			},
			processAlive: func(int) bool { return alive },
			signalProcess: func(int, syscall.Signal) error {
				signaled = true
				alive = false
				return nil
			},
		}
		if err := stopCodexDaemonLiveUpdaterWithDeps(context.Background(), codexHome, deps); err != nil {
			t.Fatalf("stop verified wrapped updater error=%v", err)
		}
		if !signaled {
			t.Fatal("verified wrapped updater was not signaled")
		}
	})

	tests := []struct {
		name     string
		identity codexProcessIdentity
		args     []string
	}{
		{name: "uid mismatch", identity: codexProcessIdentity{uid: verifiedIdentity.uid + 1, pgid: 101, start: "verified-start"}, args: verifiedArgs},
		{name: "pgid mismatch", identity: codexProcessIdentity{uid: verifiedIdentity.uid, pgid: 202, start: "verified-start"}, args: verifiedArgs},
		{name: "start mismatch", identity: codexProcessIdentity{uid: verifiedIdentity.uid, pgid: 101, start: "other-start"}, args: verifiedArgs},
		{name: "command mismatch", identity: verifiedIdentity, args: []string{"/Applications/Codex.app/codex", "app-server", "daemon", "pid-update-loop"}},
		{name: "node script mismatch", identity: verifiedIdentity, args: []string{"/usr/bin/node", "/safe/codex", "app-server", "daemon", "pid-update-loop"}},
		{name: "extra argument", identity: verifiedIdentity, args: append(append([]string{}, verifiedArgs...), "--unexpected")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var signaled bool
			deps := codexDaemonLiveUpdaterStopDeps{
				readRecord:     func(string) (codexDaemonPIDRecord, error) { return record, nil },
				inspectProcess: func(int) (codexProcessIdentity, error) { return test.identity, nil },
				readArgs:       func(int) ([]string, error) { return test.args, nil },
				processAlive:   func(int) bool { return true },
				signalProcess: func(int, syscall.Signal) error {
					signaled = true
					return nil
				},
			}
			if err := stopCodexDaemonLiveUpdaterWithDeps(context.Background(), codexHome, deps); err == nil {
				t.Fatal("stop updater error=nil, want identity rejection")
			}
			if signaled {
				t.Fatal("identity mismatch must not signal the process")
			}
		})
	}

	t.Run("argv read failure", func(t *testing.T) {
		wantErr := errors.New("argv denied")
		var signaled bool
		deps := codexDaemonLiveUpdaterStopDeps{
			readRecord:     func(string) (codexDaemonPIDRecord, error) { return record, nil },
			inspectProcess: func(int) (codexProcessIdentity, error) { return verifiedIdentity, nil },
			readArgs:       func(int) ([]string, error) { return nil, wantErr },
			processAlive:   func(int) bool { return true },
			signalProcess: func(int, syscall.Signal) error {
				signaled = true
				return nil
			},
		}
		err := stopCodexDaemonLiveUpdaterWithDeps(context.Background(), codexHome, deps)
		if !errors.Is(err, wantErr) {
			t.Fatalf("stop updater error=%v, want argv cause", err)
		}
		if signaled {
			t.Fatal("argv read failure must not signal the process")
		}
	})
}

func TestRunCodexDaemonLiveCleanupStepsAttemptsUpdaterAfterDaemonFailure(t *testing.T) {
	daemonErr := errors.New("daemon cleanup failed")
	updaterErr := errors.New("updater cleanup failed")
	updaterCalled := false
	err := runCodexDaemonLiveCleanupSteps(codexDaemonLiveCleanupSteps{
		stopDaemon: func() error { return daemonErr },
		stopUpdater: func() error {
			updaterCalled = true
			return updaterErr
		},
	})

	if !updaterCalled {
		t.Fatal("updater cleanup was skipped after daemon cleanup failed")
	}
	if !errors.Is(err, daemonErr) || !errors.Is(err, updaterErr) {
		t.Fatalf("cleanup error=%v, want both daemon and updater failures", err)
	}
}

func TestCodexDaemonLiveUpdaterArgsMatchAcceptsPackageEntrypointSymlink(t *testing.T) {
	home, packageRoot := writeCodexDaemonLiveTestPackage(t)
	managedBinary := codexDaemonManagedBinaryPath(home)
	realBinary := filepath.Join(packageRoot, "bin", "codex")
	args := []string{realBinary, "app-server", "daemon", "pid-update-loop"}
	if !codexDaemonLiveUpdaterArgsMatch(args, managedBinary) {
		t.Fatalf("argv=%q should match managed symlink %s", args, managedBinary)
	}
}

func writeCodexDaemonLiveTestPackage(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	standaloneRoot := filepath.Join(home, "packages", "standalone")
	packageRoot := filepath.Join(standaloneRoot, "releases", "v-test")
	writeCodexDaemonLiveTestFile(t, filepath.Join(packageRoot, "bin", "codex"), "codex", 0o755)
	writeCodexDaemonLiveTestFile(t, filepath.Join(packageRoot, "bin", "codex-code-mode-host"), "code-mode", 0o755)
	writeCodexDaemonLiveTestFile(t, filepath.Join(packageRoot, "codex-path", "rg"), "rg", 0o755)
	writeCodexDaemonLiveTestFile(t, filepath.Join(packageRoot, "codex-resources", "zsh", "bin", "zsh"), "zsh", 0o755)
	writeCodexDaemonLiveTestFile(t, filepath.Join(packageRoot, "codex-package.json"), `{
  "layoutVersion": 1,
  "version": "v-test",
  "target": "test-target",
  "variant": "codex",
  "entrypoint": "bin/codex",
  "resourcesDir": "codex-resources",
  "pathDir": "codex-path"
}`, 0o644)
	if err := os.Symlink(filepath.Join("bin", "codex"), filepath.Join(packageRoot, "codex")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(standaloneRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("releases", "v-test"), filepath.Join(standaloneRoot, "current")); err != nil {
		t.Fatal(err)
	}
	return home, packageRoot
}

func writeCodexDaemonLiveTestFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
