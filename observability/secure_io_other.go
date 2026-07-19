//go:build !unix

package observability

import (
	"errors"
	"fmt"
	"os"
)

func validateTraceOwner(os.FileInfo) error { return nil }

func openTraceFileNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := validateTraceFileInfo(info); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func appendTraceLineNoFollow(path string, line []byte, maxBytes int64, backups int) error {
	info, err := os.Lstat(path)
	if err == nil {
		if err := validateTraceFileInfo(info); err != nil {
			return err
		}
		if info.Size()+int64(len(line)) > maxBytes {
			if err := rotateTraceFiles(path, backups); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(line); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func rotateTraceFiles(path string, backups int) error {
	if backups <= 0 {
		return os.Remove(path)
	}
	if err := os.Remove(fmt.Sprintf("%s.%d", path, backups)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for index := backups - 1; index >= 1; index-- {
		if err := os.Rename(fmt.Sprintf("%s.%d", path, index), fmt.Sprintf("%s.%d", path, index+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(path, path+".1")
}
