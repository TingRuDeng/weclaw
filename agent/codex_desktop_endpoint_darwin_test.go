//go:build darwin

package agent

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCodexDesktopEndpointUsesCodexHomeSocket(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	want := filepath.Join(codexHome, "ipc", "ipc.sock")
	got, err := codexDesktopCurrentEndpointPath()
	if err != nil || got != want {
		t.Fatalf("codexDesktopCurrentEndpointPath() = %q, %v, want %q, nil", got, err, want)
	}
}

func TestCodexDesktopEndpointRejectsRelativeCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "relative-codex-home")
	if _, err := codexDesktopCurrentEndpointPath(); err == nil {
		t.Fatal("codexDesktopCurrentEndpointPath() error = nil, want relative path rejection")
	}
}

func TestCodexDesktopEndpointCandidatesKeepLegacyFallback(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	candidates, err := codexDesktopEndpointCandidates()
	if err != nil {
		t.Fatal(err)
	}
	want := []codexDesktopEndpointCandidate{
		{name: "current", path: filepath.Join(codexHome, "ipc", "ipc.sock"), strictPermissions: true},
		{name: "legacy", path: codexDesktopLegacyEndpointPath()},
	}
	if !reflect.DeepEqual(candidates, want) {
		t.Fatalf("codexDesktopEndpointCandidates() = %#v, want %#v", candidates, want)
	}
}

func TestDialCodexDesktopEndpointPrefersCurrentSocket(t *testing.T) {
	current := filepath.Join(t.TempDir(), "current", "ipc.sock")
	legacy := filepath.Join(t.TempDir(), "legacy", "ipc.sock")
	infos := codexDesktopTestEndpointInfos(current, legacy)
	var dialed []string
	deps := codexDesktopTestEndpointDeps(current, legacy, infos, func(_ context.Context, path string) (net.Conn, error) {
		dialed = append(dialed, path)
		return codexDesktopTestConnection(), nil
	})
	conn, err := dialCodexDesktopEndpointWithDeps(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if want := []string{current}; !reflect.DeepEqual(dialed, want) {
		t.Fatalf("dialed = %v, want %v", dialed, want)
	}
}

func TestDialCodexDesktopEndpointFallsBackToConnectableLegacySocket(t *testing.T) {
	current := filepath.Join(t.TempDir(), "current", "ipc.sock")
	legacy := filepath.Join(t.TempDir(), "legacy", "ipc.sock")
	infos := codexDesktopTestEndpointInfos(current, legacy)
	var dialed []string
	deps := codexDesktopTestEndpointDeps(current, legacy, infos, func(_ context.Context, path string) (net.Conn, error) {
		dialed = append(dialed, path)
		if path == current {
			return nil, syscall.ECONNREFUSED
		}
		return codexDesktopTestConnection(), nil
	})
	conn, err := dialCodexDesktopEndpointWithDeps(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if want := []string{current, legacy}; !reflect.DeepEqual(dialed, want) {
		t.Fatalf("dialed = %v, want %v", dialed, want)
	}
}

func TestCodexDesktopEndpointProbeIgnoresStaleLegacySocket(t *testing.T) {
	current := filepath.Join(t.TempDir(), "current", "ipc.sock")
	legacy := filepath.Join(t.TempDir(), "legacy", "ipc.sock")
	infos := codexDesktopTestEndpointInfos(current, legacy)
	delete(infos, current)
	delete(infos, filepath.Dir(current))
	deps := codexDesktopTestEndpointDeps(current, legacy, infos, func(context.Context, string) (net.Conn, error) {
		return nil, syscall.ECONNREFUSED
	})
	present, err := codexDesktopEndpointConnectableWithDeps(context.Background(), deps)
	if err != nil || present {
		t.Fatalf("codexDesktopEndpointConnectableWithDeps() = %v, %v, want false, nil", present, err)
	}
}

func TestCodexDesktopCurrentEndpointRejectsOpenPermissions(t *testing.T) {
	current := filepath.Join(t.TempDir(), "current", "ipc.sock")
	legacy := filepath.Join(t.TempDir(), "legacy", "ipc.sock")
	infos := codexDesktopTestEndpointInfos(current, legacy)
	infos[current] = codexDesktopFakeFileInfo{mode: os.ModeSocket | 0o660, uid: uint32(os.Getuid())}
	called := false
	deps := codexDesktopTestEndpointDeps(current, legacy, infos, func(context.Context, string) (net.Conn, error) {
		called = true
		return codexDesktopTestConnection(), nil
	})
	_, err := dialCodexDesktopEndpointWithDeps(context.Background(), deps)
	if !errors.Is(err, errCodexDesktopEndpointUnsafe) || called {
		t.Fatalf("dial error = %v, called = %v, want unsafe rejection before dial", err, called)
	}
}

func TestCodexDesktopEndpointRejectsNonSocket(t *testing.T) {
	deps := codexDesktopEndpointDeps{
		lstat: func(string) (os.FileInfo, error) {
			return codexDesktopFakeFileInfo{mode: 0, uid: uint32(os.Getuid())}, nil
		},
		uid: os.Getuid,
	}

	err := validateCodexDesktopEndpoint("/tmp/not-a-socket", deps)
	if !errors.Is(err, ErrCodexDesktopUnavailable) {
		t.Fatalf("validateCodexDesktopEndpoint() error = %v, want unavailable", err)
	}
}

func TestCodexDesktopEndpointRejectsDifferentUID(t *testing.T) {
	deps := codexDesktopEndpointDeps{
		lstat: func(string) (os.FileInfo, error) {
			return codexDesktopFakeFileInfo{mode: os.ModeSocket, uid: uint32(os.Getuid() + 1)}, nil
		},
		uid: os.Getuid,
	}

	err := validateCodexDesktopEndpoint("/tmp/foreign.sock", deps)
	if !errors.Is(err, ErrCodexDesktopUnavailable) {
		t.Fatalf("validateCodexDesktopEndpoint() error = %v, want unavailable", err)
	}
}

func TestCodexDesktopEndpointMissingIsUnavailable(t *testing.T) {
	deps := codexDesktopEndpointDeps{
		lstat: func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist },
		uid:   os.Getuid,
	}
	err := validateCodexDesktopEndpoint("/tmp/missing.sock", deps)
	if !errors.Is(err, ErrCodexDesktopUnavailable) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("validateCodexDesktopEndpoint() error = %v, want unavailable and not-exist", err)
	}
}

func TestCodexDesktopEndpointRejectsSymlink(t *testing.T) {
	deps := codexDesktopEndpointDeps{
		lstat: func(string) (os.FileInfo, error) {
			return codexDesktopFakeFileInfo{mode: os.ModeSymlink, uid: uint32(os.Getuid())}, nil
		},
		uid: os.Getuid,
	}
	if err := validateCodexDesktopEndpoint("/tmp/link.sock", deps); !errors.Is(err, ErrCodexDesktopUnavailable) {
		t.Fatalf("validateCodexDesktopEndpoint() error = %v, want unavailable", err)
	}
}

func TestCodexDesktopPresenceReportsEndpointAndProcessState(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     bool
		process      bool
		wantEndpoint bool
		wantProcess  bool
	}{
		{name: "both missing"},
		{name: "endpoint only", endpoint: true, wantEndpoint: true},
		{name: "process only", process: true, wantProcess: true},
		{name: "both present", endpoint: true, process: true, wantEndpoint: true, wantProcess: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint, process := codexDesktopPresenceWithDeps(codexDesktopPresenceDeps{
				probe:          func(context.Context) (bool, error) { return test.endpoint, nil },
				processRunning: func() (bool, error) { return test.process, nil },
				timeout:        time.Second,
			})
			if endpoint != test.wantEndpoint || process != test.wantProcess {
				t.Fatalf("presence = %v, %v, want %v, %v", endpoint, process, test.wantEndpoint, test.wantProcess)
			}
		})
	}
}

func TestCodexDesktopPresenceFailsClosedOnProbeError(t *testing.T) {
	endpoint, process := codexDesktopPresenceWithDeps(codexDesktopPresenceDeps{
		probe:          func(context.Context) (bool, error) { return false, errors.New("probe failed") },
		processRunning: func() (bool, error) { return false, nil },
		timeout:        time.Second,
	})
	if !endpoint || !process {
		t.Fatalf("presence = %v, %v, want conservative true, true", endpoint, process)
	}
}

func TestCodexDesktopProcessPresentRecognizesCurrentAndLegacyNames(t *testing.T) {
	tests := []struct {
		name        string
		processName string
		uid         uint32
		want        bool
	}{
		{name: "current ChatGPT", processName: "ChatGPT", uid: uint32(os.Getuid()), want: true},
		{name: "legacy Codex", processName: "Codex", uid: uint32(os.Getuid()), want: true},
		{name: "helper process", processName: "ChatGPT Helper", uid: uint32(os.Getuid())},
		{name: "other user", processName: "ChatGPT", uid: uint32(os.Getuid() + 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			found, err := codexDesktopProcessPresentFrom(func(string, ...int) ([]unix.KinfoProc, error) {
				return []unix.KinfoProc{codexDesktopTestProcess(test.processName, test.uid)}, nil
			})
			if err != nil || found != test.want {
				t.Fatalf("codexDesktopProcessPresentFrom() = %v, %v, want %v, nil", found, err, test.want)
			}
		})
	}
}

func TestDialCodexDesktopEndpointRejectsBeforeDial(t *testing.T) {
	called := false
	deps := codexDesktopEndpointDeps{
		candidates: func() ([]codexDesktopEndpointCandidate, error) {
			return []codexDesktopEndpointCandidate{{name: "current", path: "/tmp/missing.sock"}}, nil
		},
		lstat: func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist },
		uid:   os.Getuid,
		dial: func(context.Context, string) (net.Conn, error) {
			called = true
			return nil, nil
		},
	}
	_, err := dialCodexDesktopEndpointWithDeps(context.Background(), deps)
	if !errors.Is(err, ErrCodexDesktopUnavailable) || called {
		t.Fatalf("dial error = %v, called = %v", err, called)
	}
}

func codexDesktopTestEndpointDeps(
	current string,
	legacy string,
	infos map[string]os.FileInfo,
	dial func(context.Context, string) (net.Conn, error),
) codexDesktopEndpointDeps {
	return codexDesktopEndpointDeps{
		candidates: func() ([]codexDesktopEndpointCandidate, error) {
			return []codexDesktopEndpointCandidate{
				{name: "current", path: current, strictPermissions: true},
				{name: "legacy", path: legacy},
			}, nil
		},
		lstat: func(path string) (os.FileInfo, error) {
			info, ok := infos[path]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return info, nil
		},
		uid:  os.Getuid,
		dial: dial,
	}
}

func codexDesktopTestEndpointInfos(current string, legacy string) map[string]os.FileInfo {
	uid := uint32(os.Getuid())
	return map[string]os.FileInfo{
		filepath.Dir(current): codexDesktopFakeFileInfo{mode: os.ModeDir | 0o700, uid: uid},
		current:               codexDesktopFakeFileInfo{mode: os.ModeSocket | 0o600, uid: uid},
		filepath.Dir(legacy):  codexDesktopFakeFileInfo{mode: os.ModeDir | 0o755, uid: uid},
		legacy:                codexDesktopFakeFileInfo{mode: os.ModeSocket | 0o755, uid: uid},
	}
}

func codexDesktopTestConnection() net.Conn {
	left, right := net.Pipe()
	_ = right.Close()
	return left
}

func codexDesktopTestProcess(name string, uid uint32) unix.KinfoProc {
	var process unix.KinfoProc
	copy(process.Proc.P_comm[:], []byte(name))
	process.Eproc.Ucred.Uid = uid
	return process
}

type codexDesktopFakeFileInfo struct {
	mode os.FileMode
	uid  uint32
}

func (f codexDesktopFakeFileInfo) Name() string       { return "ipc.sock" }
func (f codexDesktopFakeFileInfo) Size() int64        { return 0 }
func (f codexDesktopFakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f codexDesktopFakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f codexDesktopFakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f codexDesktopFakeFileInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }
