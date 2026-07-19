//go:build unix

package codexauth

import (
	"fmt"
	"os"
	"syscall"
)

func validateCurrentOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot verify secure file owner")
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("secure file is not owned by the current user")
	}
	return nil
}
