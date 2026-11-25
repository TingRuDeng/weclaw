//go:build unix

package antigravity

import (
	"fmt"
	"os"
	"syscall"
)

func validateSocketOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: cannot verify owner", ErrUnsafeSocket)
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: owner mismatch", ErrUnsafeSocket)
	}
	return nil
}

func transcriptFileID(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}
