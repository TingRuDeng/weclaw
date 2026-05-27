//go:build !windows

package cmd

import "syscall"

func signalProcessGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
