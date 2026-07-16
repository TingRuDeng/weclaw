//go:build darwin || linux

package cmd

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func rebindDaemonStandardFiles(file *os.File) error {
	if err := unix.Dup2(int(file.Fd()), int(os.Stdout.Fd())); err != nil {
		return fmt.Errorf("rebind daemon stdout: %w", err)
	}
	if err := unix.Dup2(int(file.Fd()), int(os.Stderr.Fd())); err != nil {
		return fmt.Errorf("rebind daemon stderr: %w", err)
	}
	return nil
}
