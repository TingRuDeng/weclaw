//go:build unix

package agent

import (
	"fmt"
	"os"
	"syscall"
)

func validateCodexProviderOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("Codex provider 状态必须由当前用户持有")
	}
	return nil
}
