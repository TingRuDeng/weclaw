//go:build darwin

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestConfigureCodexAppDaemonReuseSetsLaunchEnvironmentAfterSocketMatch(t *testing.T) {
	home := "/Users/test"
	environment := map[string]string{}
	var actions [][]string
	deps := codexAppDaemonReuseDeps{
		launchEnvironment: func(_ context.Context, name string) (string, error) {
			return environment[name], nil
		},
		launchctl: func(_ context.Context, args ...string) (string, error) {
			actions = append(actions, append([]string(nil), args...))
			return "", nil
		},
		inspect: func(context.Context) (codexAppDaemonReuseResult, error) {
			return codexAppDaemonReuseResult{}, nil
		},
		userHome: func() (string, error) { return home, nil },
	}
	socketPath := codexDaemonSocketPath(filepath.Join(home, ".codex"))
	result, err := configureCodexAppDaemonReuseWithDeps(context.Background(), true, socketPath, deps)
	if err != nil || !result.Changed {
		t.Fatalf("result=%#v error=%v, want changed enablement", result, err)
	}
	want := [][]string{{"setenv", codexAppUseLocalDaemonEnv, "1"}}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions=%#v, want %#v", actions, want)
	}
}

func TestConfigureCodexAppDaemonReusePropagatesSQLiteHome(t *testing.T) {
	home := "/Users/test"
	sqliteHome := "/Users/test/.weclaw/codex-sqlite"
	environment := map[string]string{}
	var actions [][]string
	expected := &codexAppDaemonEnvironment{
		CodexHome:        filepath.Join(home, ".codex"),
		CodexSQLiteHome:  sqliteHome,
		DefaultCodexHome: filepath.Join(home, ".codex"),
	}
	deps := codexAppDaemonReuseDeps{
		launchEnvironment: func(_ context.Context, name string) (string, error) {
			return environment[name], nil
		},
		launchctl: func(_ context.Context, args ...string) (string, error) {
			actions = append(actions, append([]string(nil), args...))
			if len(args) == 3 && args[0] == "setenv" {
				environment[args[1]] = args[2]
			}
			return "", nil
		},
		inspect: func(context.Context) (codexAppDaemonReuseResult, error) {
			return codexAppDaemonReuseResult{}, nil
		},
		userHome:            func() (string, error) { return home, nil },
		expectedEnvironment: expected,
	}
	result, err := configureCodexAppDaemonReuseWithDeps(
		context.Background(), true,
		codexDaemonSocketPath(filepath.Join(home, ".codex")), deps,
	)
	if err != nil || !result.Changed {
		t.Fatalf("result=%#v error=%v, want changed enablement", result, err)
	}
	want := [][]string{
		{"setenv", codexAppSQLiteHomeEnv, sqliteHome},
		{"setenv", codexAppUseLocalDaemonEnv, "1"},
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions=%#v, want %#v", actions, want)
	}
}

func TestConfigureCodexAppDaemonReusePropagatesCustomCodexHome(t *testing.T) {
	environment := map[string]string{}
	var actions [][]string
	expected := &codexAppDaemonEnvironment{
		CodexHome:        "/Users/test/.weclaw-codex",
		CodexSQLiteHome:  "/Users/test/.weclaw/codex-sqlite",
		DefaultCodexHome: "/Users/test/.codex",
	}
	deps := codexAppDaemonReuseDeps{
		launchEnvironment: func(_ context.Context, name string) (string, error) {
			return environment[name], nil
		},
		launchctl: func(_ context.Context, args ...string) (string, error) {
			actions = append(actions, append([]string(nil), args...))
			if len(args) == 3 && args[0] == "setenv" {
				environment[args[1]] = args[2]
			}
			return "", nil
		},
		inspect:             func(context.Context) (codexAppDaemonReuseResult, error) { return codexAppDaemonReuseResult{}, nil },
		userHome:            func() (string, error) { return "/Users/test", nil },
		expectedEnvironment: expected,
	}
	result, err := configureCodexAppDaemonReuseWithDeps(
		context.Background(), true,
		codexDaemonSocketPath(expected.CodexHome), deps,
	)
	if err != nil || !result.Changed {
		t.Fatalf("result=%#v error=%v, want changed enablement", result, err)
	}
	want := [][]string{
		{"setenv", codexAppCodexHomeEnv, expected.CodexHome},
		{"setenv", codexAppSQLiteHomeEnv, expected.CodexSQLiteHome},
		{"setenv", codexAppUseLocalDaemonEnv, "1"},
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions=%#v, want %#v", actions, want)
	}
}

func TestConfigureCodexAppDaemonReuseRollsBackPartialLaunchEnvironmentUpdate(t *testing.T) {
	environment := map[string]string{}
	var actions [][]string
	failedSQLiteSet := false
	inspectCalled := false
	deps := codexAppDaemonReuseDeps{
		launchEnvironment: func(_ context.Context, name string) (string, error) {
			return environment[name], nil
		},
		launchctl: func(_ context.Context, args ...string) (string, error) {
			actions = append(actions, append([]string(nil), args...))
			if len(args) == 3 && args[0] == "setenv" && args[1] == codexAppSQLiteHomeEnv && !failedSQLiteSet {
				failedSQLiteSet = true
				return "", errors.New("simulated launchctl failure")
			}
			switch {
			case len(args) == 3 && args[0] == "setenv":
				environment[args[1]] = args[2]
			case len(args) == 2 && args[0] == "unsetenv":
				delete(environment, args[1])
			}
			return "", nil
		},
		inspect: func(context.Context) (codexAppDaemonReuseResult, error) {
			inspectCalled = true
			return codexAppDaemonReuseResult{}, nil
		},
		userHome: func() (string, error) { return "/Users/test", nil },
		expectedEnvironment: &codexAppDaemonEnvironment{
			CodexHome:        "/Users/test/.weclaw-codex",
			CodexSQLiteHome:  "/Users/test/.weclaw/codex-sqlite",
			DefaultCodexHome: "/Users/test/.codex",
		},
	}
	result, err := configureCodexAppDaemonReuseWithDeps(
		context.Background(), true,
		codexDaemonSocketPath("/Users/test/.weclaw-codex"), deps,
	)
	if err == nil || result.Changed || inspectCalled {
		t.Fatalf("result=%#v error=%v inspectCalled=%v, want failed uncompromised update", result, err, inspectCalled)
	}
	if len(environment) != 0 {
		t.Fatalf("environment=%#v, want all launchd values restored", environment)
	}
	want := [][]string{
		{"setenv", codexAppCodexHomeEnv, "/Users/test/.weclaw-codex"},
		{"setenv", codexAppSQLiteHomeEnv, "/Users/test/.weclaw/codex-sqlite"},
		{"unsetenv", codexAppSQLiteHomeEnv},
		{"unsetenv", codexAppCodexHomeEnv},
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions=%#v, want %#v", actions, want)
	}
}

func TestConfigureCodexAppDaemonReuseRejectsConflictingSQLiteHomeBeforeMutation(t *testing.T) {
	var actions [][]string
	deps := codexAppDaemonReuseDeps{
		launchEnvironment: func(_ context.Context, name string) (string, error) {
			switch name {
			case "CODEX_SQLITE_HOME":
				return "/Users/test/.codex", nil
			default:
				return "", nil
			}
		},
		launchctl: func(_ context.Context, args ...string) (string, error) {
			actions = append(actions, append([]string(nil), args...))
			return "", nil
		},
		inspect:  func(context.Context) (codexAppDaemonReuseResult, error) { return codexAppDaemonReuseResult{}, nil },
		userHome: func() (string, error) { return "/Users/test", nil },
		expectedEnvironment: &codexAppDaemonEnvironment{
			CodexHome:        "/Users/test/.codex",
			CodexSQLiteHome:  "/Users/test/.weclaw/codex-sqlite",
			DefaultCodexHome: "/Users/test/.codex",
		},
	}
	_, err := configureCodexAppDaemonReuseWithDeps(
		context.Background(), true,
		codexDaemonSocketPath("/Users/test/.codex"), deps,
	)
	if err == nil || !strings.Contains(err.Error(), codexAppSQLiteHomeEnv) || len(actions) != 0 {
		t.Fatalf("error=%v actions=%#v, want SQLite mismatch before mutation", err, actions)
	}
}

func TestConfigureCodexAppDaemonReuseRejectsSocketMismatchBeforeMutation(t *testing.T) {
	var actions [][]string
	deps := codexAppDaemonReuseDeps{
		launchEnvironment: func(_ context.Context, name string) (string, error) {
			if name == "CODEX_HOME" {
				return "/Users/app/.codex", nil
			}
			return "", nil
		},
		launchctl: func(_ context.Context, args ...string) (string, error) {
			actions = append(actions, append([]string(nil), args...))
			return "", nil
		},
		inspect:  func(context.Context) (codexAppDaemonReuseResult, error) { return codexAppDaemonReuseResult{}, nil },
		userHome: func() (string, error) { return "/Users/test", nil },
	}
	_, err := configureCodexAppDaemonReuseWithDeps(context.Background(), true, "/Users/weclaw/.codex/app-server-control/app-server-control.sock", deps)
	if err == nil || !strings.Contains(err.Error(), "does not match") || len(actions) != 0 {
		t.Fatalf("error=%v actions=%#v, want mismatch before mutation", err, actions)
	}
}

func TestConfigureCodexAppDaemonReuseExplicitDisableUnsetsEnvironment(t *testing.T) {
	var actions [][]string
	deps := codexAppDaemonReuseDeps{
		launchEnvironment: func(context.Context, string) (string, error) { return "1", nil },
		launchctl: func(_ context.Context, args ...string) (string, error) {
			actions = append(actions, append([]string(nil), args...))
			return "", nil
		},
		inspect:  func(context.Context) (codexAppDaemonReuseResult, error) { return codexAppDaemonReuseResult{}, nil },
		userHome: func() (string, error) { return "/Users/test", nil },
	}
	result, err := configureCodexAppDaemonReuseWithDeps(context.Background(), false, "", deps)
	want := [][]string{{"unsetenv", codexAppUseLocalDaemonEnv}}
	if err != nil || !result.Changed || !reflect.DeepEqual(actions, want) {
		t.Fatalf("result=%#v error=%v actions=%#v, want explicit unset", result, err, actions)
	}
}

func TestInspectCodexAppDaemonReuseAcceptsDaemonTransportWithoutDesktopEndpoint(t *testing.T) {
	result, err := inspectCodexAppDaemonReuseWithDeps(codexAppDaemonInspectDeps{
		hostState: func() (codexDesktopHostProcessState, error) {
			return codexDesktopHostProcessState{AppRunning: true, AppPIDs: []int{100}}, nil
		},
		processEnvironment: func(pid int, name string) (string, bool, error) {
			if pid != 100 || name != codexAppUseLocalDaemonEnv {
				t.Fatalf("pid=%d name=%q", pid, name)
			}
			return "1", true, nil
		},
	})
	if err != nil || !result.AppRunning || result.PrivateAppServer {
		t.Fatalf("result=%#v error=%v, want verified daemon frontend", result, err)
	}
}

func TestInspectCodexAppDaemonReuseRejectsAppWithoutDaemonEnvironment(t *testing.T) {
	result, err := inspectCodexAppDaemonReuseWithDeps(codexAppDaemonInspectDeps{
		hostState: func() (codexDesktopHostProcessState, error) {
			return codexDesktopHostProcessState{AppRunning: true, AppPIDs: []int{100}}, nil
		},
		processEnvironment: func(int, string) (string, bool, error) {
			return "", false, nil
		},
	})
	if err == nil || !result.AppRunning || !strings.Contains(err.Error(), codexAppUseLocalDaemonEnv) {
		t.Fatalf("result=%#v error=%v, want missing daemon environment rejection", result, err)
	}
}

func TestInspectCodexAppDaemonReuseRejectsRunningAppWithDifferentSQLiteHome(t *testing.T) {
	result, err := inspectCodexAppDaemonReuseWithDeps(codexAppDaemonInspectDeps{
		hostState: func() (codexDesktopHostProcessState, error) {
			return codexDesktopHostProcessState{AppRunning: true, AppPIDs: []int{100}}, nil
		},
		processEnvironment: func(pid int, name string) (string, bool, error) {
			if pid != 100 {
				t.Fatalf("pid=%d", pid)
			}
			switch name {
			case codexAppUseLocalDaemonEnv:
				return "1", true, nil
			case "CODEX_HOME":
				return "/Users/test/.codex", true, nil
			case codexAppSQLiteHomeEnv:
				return "/Users/test/.codex", true, nil
			default:
				t.Fatalf("unexpected environment name %q", name)
				return "", false, nil
			}
		},
		expectedEnvironment: &codexAppDaemonEnvironment{
			CodexHome:        "/Users/test/.codex",
			CodexSQLiteHome:  "/Users/test/.weclaw/codex-sqlite",
			DefaultCodexHome: "/Users/test/.codex",
		},
	})
	if err == nil || !result.AppRunning || !strings.Contains(err.Error(), codexAppSQLiteHomeEnv) ||
		!errors.Is(err, ErrCodexAppRestartRequired) {
		t.Fatalf("result=%#v error=%v, want running App SQLite mismatch", result, err)
	}
}

func TestInspectCodexAppDaemonReuseAcceptsMatchingPathEnvironment(t *testing.T) {
	expected := &codexAppDaemonEnvironment{
		CodexHome:        "/Users/test/.codex",
		CodexSQLiteHome:  "/Users/test/.weclaw/codex-sqlite",
		DefaultCodexHome: "/Users/test/.codex",
	}
	result, err := inspectCodexAppDaemonReuseWithDeps(codexAppDaemonInspectDeps{
		hostState: func() (codexDesktopHostProcessState, error) {
			return codexDesktopHostProcessState{AppRunning: true, AppPIDs: []int{100}}, nil
		},
		processEnvironment: func(pid int, name string) (string, bool, error) {
			if pid != 100 {
				t.Fatalf("pid=%d", pid)
			}
			switch name {
			case codexAppUseLocalDaemonEnv:
				return "1", true, nil
			case codexAppCodexHomeEnv:
				return expected.CodexHome, true, nil
			case codexAppSQLiteHomeEnv:
				return expected.CodexSQLiteHome, true, nil
			default:
				t.Fatalf("unexpected environment name %q", name)
				return "", false, nil
			}
		},
		expectedEnvironment: expected,
	})
	if err != nil || !result.AppRunning || result.PrivateAppServer {
		t.Fatalf("result=%#v error=%v, want matching shared environment", result, err)
	}
}

func TestInspectCodexAppDaemonReuseRejectsLaunchdPathDrift(t *testing.T) {
	expected := &codexAppDaemonEnvironment{
		CodexHome:        "/Users/test/.codex",
		CodexSQLiteHome:  "/Users/test/.weclaw/codex-sqlite",
		DefaultCodexHome: "/Users/test/.codex",
	}
	result, err := inspectCodexAppDaemonReuseWithDeps(codexAppDaemonInspectDeps{
		hostState: func() (codexDesktopHostProcessState, error) {
			return codexDesktopHostProcessState{AppRunning: true, AppPIDs: []int{100}}, nil
		},
		processEnvironment: func(int, string) (string, bool, error) {
			t.Fatal("process environment should not be inspected after launchd drift")
			return "", false, nil
		},
		launchEnvironment: func(_ context.Context, name string) (string, error) {
			switch name {
			case codexAppCodexHomeEnv:
				return expected.CodexHome, nil
			case codexAppSQLiteHomeEnv:
				return "/Users/test/.codex", nil
			default:
				return "", nil
			}
		},
		userHome:            func() (string, error) { return "/Users/test", nil },
		expectedEnvironment: expected,
	})
	if err == nil || !result.AppRunning || !errors.Is(err, ErrCodexAppRestartRequired) ||
		!strings.Contains(err.Error(), codexAppSQLiteHomeEnv) {
		t.Fatalf("result=%#v error=%v, want launchd drift requiring restart", result, err)
	}
}

func TestCodexDesktopHostProcessStateFindsPrivateAppServer(t *testing.T) {
	uid := uint32(os.Getuid())
	processes := []unix.KinfoProc{
		codexAppDaemonTestProcess("ChatGPT", uid, 100, 1),
		codexAppDaemonTestProcess("ChatGPT Helper", uid, 101, 100),
		codexAppDaemonTestProcess("codex", uid, 102, 101),
		codexAppDaemonTestProcess("codex", uid, 200, 1),
	}
	state, err := codexDesktopHostProcessStateFrom(processes, func(pid int) (string, error) {
		switch pid {
		case 102:
			return "/Applications/ChatGPT.app/Contents/Resources/codex app-server --stdio", nil
		case 200:
			return "/usr/local/bin/codex app-server", nil
		default:
			return "", errors.New("unexpected pid")
		}
	})
	if err != nil || !state.AppRunning || !state.PrivateAppServer {
		t.Fatalf("state=%#v error=%v, want private App app-server", state, err)
	}
}

func TestCodexDesktopHostProcessStateIgnoresCodeModeHost(t *testing.T) {
	uid := uint32(os.Getuid())
	processes := []unix.KinfoProc{
		codexAppDaemonTestProcess("ChatGPT", uid, 100, 1),
		codexAppDaemonTestProcess("ChatGPT Helper", uid, 101, 100),
		codexAppDaemonTestProcess("codex", uid, 102, 101),
	}
	state, err := codexDesktopHostProcessStateFrom(processes, func(pid int) (string, error) {
		if pid != 102 {
			return "", errors.New("unexpected pid")
		}
		return "/Applications/ChatGPT.app/Contents/Resources/codex -c features.code_mode_host=true app-server --analytics-default-enabled", nil
	})
	if err != nil || !state.AppRunning || state.PrivateAppServer {
		t.Fatalf("state=%#v error=%v, Code Mode helper is not the App conversation Host", state, err)
	}
}

func TestCodexDesktopHostProcessStateIgnoresDaemonProbe(t *testing.T) {
	uid := uint32(os.Getuid())
	processes := []unix.KinfoProc{
		codexAppDaemonTestProcess("ChatGPT", uid, 100, 1),
		codexAppDaemonTestProcess("codex", uid, 101, 100),
	}
	state, err := codexDesktopHostProcessStateFrom(processes, func(int) (string, error) {
		return "/Applications/ChatGPT.app/Contents/Resources/codex app-server daemon version", nil
	})
	if err != nil || !state.AppRunning || state.PrivateAppServer {
		t.Fatalf("state=%#v error=%v, daemon version probe is not a private Host", state, err)
	}
}

func TestCodexPrivateAppServerCommand(t *testing.T) {
	tests := map[string]bool{
		"codex app-server":                                         true,
		"codex -c feature=true app-server --stdio":                 true,
		"codex -c features.code_mode_host=true app-server --stdio": false,
		"codex app-server daemon version":                          false,
		"codex app-server proxy":                                   false,
		"codex --remote unix:///tmp/codex.sock":                    false,
	}
	for command, want := range tests {
		if got := codexPrivateAppServerCommand(command); got != want {
			t.Fatalf("codexPrivateAppServerCommand(%q)=%v, want %v", command, got, want)
		}
	}
}

func codexAppDaemonTestProcess(name string, uid uint32, pid int32, ppid int32) unix.KinfoProc {
	var process unix.KinfoProc
	copy(process.Proc.P_comm[:], []byte(name))
	process.Proc.P_pid = pid
	process.Eproc.Ppid = ppid
	process.Eproc.Ucred.Uid = uid
	return process
}
