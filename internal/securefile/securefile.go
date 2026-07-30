package securefile

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

var syncDirectory = func(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

// EnsureDir creates a private directory or tightens an existing current-user directory to 0700.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create secure directory: %w", err)
	}
	info, err := inspectDirectory(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() == 0o700 {
		return nil
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("chmod secure directory: %w", err)
	}
	return nil
}

// ValidateDir rejects directories that are not private, current-user-owned real directories.
func ValidateDir(path string) error {
	info, err := inspectDirectory(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("secure directory permissions must be 0700")
	}
	return nil
}

// Read opens a protected file without following its final symlink and requires mode 0600.
func Read(path string) ([]byte, error) {
	return read(path, true)
}

// ReadForUpdate validates an existing protected file but permits a legacy mode before atomic replacement.
func ReadForUpdate(path string) ([]byte, error) {
	return read(path, false)
}

func read(path string, requirePrivateMode bool) ([]byte, error) {
	if err := ValidateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, info, err := openNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("open secure file: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := validateFileInfo(info, requirePrivateMode); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read secure file: %w", err)
	}
	return data, nil
}

// Write atomically replaces a protected file with mode 0600 and rejects unsafe existing targets.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if err := validateFileInfo(info, false); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect secure file: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".weclaw-secure-*.tmp")
	if err != nil {
		return fmt.Errorf("create secure temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod secure temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write secure temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync secure temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close secure temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace secure file: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		log.Printf("[securefile] file replaced but parent directory sync failed: %v", err)
	}
	return nil
}

func inspectDirectory(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect secure directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("secure directory is not a real directory")
	}
	if err := validateCurrentOwner(info); err != nil {
		return nil, err
	}
	return info, nil
}

func validateFileInfo(info os.FileInfo, requirePrivateMode bool) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("secure file is not a regular file")
	}
	if requirePrivateMode && info.Mode().Perm() != 0o600 {
		return fmt.Errorf("secure file permissions must be 0600")
	}
	return validateCurrentOwner(info)
}
