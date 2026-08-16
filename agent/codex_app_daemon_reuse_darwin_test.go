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
			return "/Applications/ChatGPT.app/Contents/Resources/codex -c features.code_mode_host=true app-server --analytics-default-enabled", nil
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
		"codex app-server":                         true,
		"codex -c feature=true app-server --stdio": true,
		"codex app-server daemon version":          false,
		"codex app-server proxy":                   false,
		"codex --remote unix:///tmp/codex.sock":    false,
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
