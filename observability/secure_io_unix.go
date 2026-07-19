//go:build unix

package observability

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func validateTraceOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot verify trace path owner")
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("trace path is not owned by the current user")
	}
	return nil
}

func openTraceDirectoryNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open trace directory: invalid file descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := validateTraceDirectoryInfo(info); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func openTraceFileNoFollow(path string) (*os.File, error) {
	dir, err := openTraceDirectoryNoFollow(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open trace file: invalid file descriptor")
	}
	if err := validateOpenedTraceFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func appendTraceLineNoFollow(path string, line []byte, maxBytes int64, backups int) error {
	dir, err := openTraceDirectoryNoFollow(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	dirFD := int(dir.Fd())
	base := filepath.Base(path)
	var stat unix.Stat_t
	err = unix.Fstatat(dirFD, base, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		if stat.Mode&unix.S_IFMT != unix.S_IFREG {
			return fmt.Errorf("trace file must be a regular file")
		}
		if stat.Mode&0o777 != 0o600 {
			return fmt.Errorf("trace file permissions must be 0600: %o", stat.Mode&0o777)
		}
		if int(stat.Uid) != os.Geteuid() {
			return fmt.Errorf("trace file is not owned by the current user")
		}
		if stat.Size+int64(len(line)) > maxBytes {
			if err := rotateTraceFilesAt(dirFD, base, backups); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	fd, err := unix.Openat(dirFD, base, unix.O_CREAT|unix.O_WRONLY|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("append trace file: invalid file descriptor")
	}
	if err := validateOpenedTraceFile(file); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(line); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func validateOpenedTraceFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return validateTraceFileInfo(info)
}

func rotateTraceFilesAt(dirFD int, base string, backups int) error {
	if backups <= 0 {
		return unlinkTraceAt(dirFD, base)
	}
	if err := unlinkTraceAt(dirFD, fmt.Sprintf("%s.%d", base, backups)); err != nil {
		return err
	}
	for index := backups - 1; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", base, index)
		to := fmt.Sprintf("%s.%d", base, index+1)
		if err := unix.Renameat(dirFD, from, dirFD, to); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	if err := unix.Renameat(dirFD, base, dirFD, base+".1"); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

func unlinkTraceAt(dirFD int, name string) error {
	err := unix.Unlinkat(dirFD, name, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
