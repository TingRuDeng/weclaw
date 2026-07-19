package codexauth

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func ensureSecureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create secure directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect secure directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("secure directory is not a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("secure directory permissions must be 0700")
	}
	if err := validateCurrentOwner(info); err != nil {
		return err
	}
	return nil
}

func readSecureFile(path string) ([]byte, error) {
	file, info, err := openSecureFileNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(file)
	if err := validateSecureFileInfo(info); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func atomicWriteSecureFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ensureSecureDir(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if err := validateSecureFileInfo(info); err != nil {
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
		tmp.Close()
		return fmt.Errorf("chmod secure temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write secure temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync secure temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close secure temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace secure file: %w", err)
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open secure directory for sync: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("sync secure directory: %w", err)
	}
	return nil
}

func validateSecureFileInfo(info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("secure file is not a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("secure file permissions must be 0600")
	}
	return validateCurrentOwner(info)
}

func closeQuietly(closer io.Closer) { _ = closer.Close() }
