//go:build unix

package antigravity

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

type fakeSocketInfo struct {
	mode os.FileMode
	uid  uint32
}

func (f fakeSocketInfo) Name() string       { return "socket" }
func (f fakeSocketInfo) Size() int64        { return 0 }
func (f fakeSocketInfo) Mode() os.FileMode  { return f.mode }
func (f fakeSocketInfo) ModTime() time.Time { return time.Time{} }
func (f fakeSocketInfo) IsDir() bool        { return false }
func (f fakeSocketInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

func TestValidateSocketInfoRejectsForeignOwner(t *testing.T) {
	current := uint32(os.Geteuid())
	if err := validateSocketInfo(fakeSocketInfo{mode: os.ModeSocket | 0o600, uid: current}); err != nil {
		t.Fatalf("current-user socket rejected: %v", err)
	}
	err := validateSocketInfo(fakeSocketInfo{mode: os.ModeSocket | 0o600, uid: current + 1})
	if !errors.Is(err, ErrUnsafeSocket) {
		t.Fatalf("foreign owner error = %v", err)
	}
	if err := validateSocketInfo(fakeSocketInfo{mode: os.ModeSocket | os.ModeSymlink, uid: current}); !errors.Is(err, ErrUnsafeSocket) {
		t.Fatalf("symlink error = %v", err)
	}
	if err := validateSocketInfo(fakeSocketInfo{mode: 0o600, uid: current}); !errors.Is(err, ErrUnsafeSocket) {
		t.Fatalf("regular file error = %v", err)
	}
}
