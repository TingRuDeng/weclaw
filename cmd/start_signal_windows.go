//go:build windows

package cmd

import "syscall"

func signalProcessGroup(_ int, _ syscall.Signal) error {
	return nil
}
