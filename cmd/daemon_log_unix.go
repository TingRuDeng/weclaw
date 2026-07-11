//go:build darwin || linux

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

func rebindDaemonStandardFiles(file *os.File) error {
	if err := syscall.Dup2(int(file.Fd()), int(os.Stdout.Fd())); err != nil {
		return fmt.Errorf("rebind daemon stdout: %w", err)
	}
	if err := syscall.Dup2(int(file.Fd()), int(os.Stderr.Fd())); err != nil {
		return fmt.Errorf("rebind daemon stderr: %w", err)
	}
	return nil
}
