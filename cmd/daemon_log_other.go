//go:build !darwin && !linux

package cmd

import "os"

func rebindDaemonStandardFiles(_ *os.File) error {
	return nil
}
