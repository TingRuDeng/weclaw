//go:build !unix

package securefile

import (
	"fmt"
	"os"
)

func openNoFollow(path string) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("secure file is a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !os.SameFile(before, after) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("secure file changed while opening")
	}
	return file, after, nil
}

func openAppendNoFollow(path string) (*os.File, os.FileInfo, error) {
	before, beforeErr := os.Lstat(path)
	if beforeErr == nil && before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("secure file is a symlink")
	}
	if beforeErr != nil && !os.IsNotExist(beforeErr) {
		return nil, nil, beforeErr
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, after) ||
		(beforeErr == nil && !os.SameFile(before, after)) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("secure file changed while opening")
	}
	return file, after, nil
}
