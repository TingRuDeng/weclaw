//go:build darwin

package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCodexDesktopEndpointUsesCurrentUIDSocket(t *testing.T) {
	want := filepath.Join(os.TempDir(), "codex-ipc", fmt.Sprintf("ipc-%d.sock", os.Getuid()))
	if got := codexDesktopEndpointPath(); got != want {
		t.Fatalf("codexDesktopEndpointPath() = %q, want %q", got, want)
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

func TestCodexDesktopPresenceRequiresMissingSocketAndProcess(t *testing.T) {
	tests := []struct {
		name          string
		socketPresent bool
		processFound  bool
		wantAbsent    bool
	}{
		{"both missing", false, false, true},
		{"socket remains", true, false, false},
		{"process remains", false, true, false},
		{"both remain", true, true, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := codexDesktopPresenceDeps{
				lstat:          codexDesktopPresenceLstat(test.socketPresent),
				processRunning: func() (bool, error) { return test.processFound, nil },
			}
			got, err := codexDesktopEndpointAbsent("/tmp/codex.sock", deps)
			if err != nil || got != test.wantAbsent {
				t.Fatalf("codexDesktopEndpointAbsent() = %v, %v, want %v, nil", got, err, test.wantAbsent)
			}
		})
	}
}

func TestCodexDesktopEndpointMissingIsUnavailable(t *testing.T) {
	deps := codexDesktopEndpointDeps{
		lstat: func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist },
		uid:   os.Getuid,
	}
	if err := validateCodexDesktopEndpoint("/tmp/missing.sock", deps); !errors.Is(err, ErrCodexDesktopUnavailable) {
		t.Fatalf("validateCodexDesktopEndpoint() error = %v, want unavailable", err)
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

func codexDesktopPresenceLstat(present bool) func(string) (os.FileInfo, error) {
	return func(string) (os.FileInfo, error) {
		if !present {
			return nil, fs.ErrNotExist
		}
		return codexDesktopFakeFileInfo{mode: os.ModeSocket, uid: uint32(os.Getuid())}, nil
	}
}

type codexDesktopFakeFileInfo struct {
	mode os.FileMode
	uid  uint32
}

func (f codexDesktopFakeFileInfo) Name() string       { return "ipc.sock" }
func (f codexDesktopFakeFileInfo) Size() int64        { return 0 }
func (f codexDesktopFakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f codexDesktopFakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f codexDesktopFakeFileInfo) IsDir() bool        { return false }
func (f codexDesktopFakeFileInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

func TestDialCodexDesktopEndpointRejectsBeforeDial(t *testing.T) {
	called := false
	deps := codexDesktopEndpointDeps{
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
