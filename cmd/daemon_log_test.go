package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonLogWriterRotatesBySize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weclaw.log")
	writer, err := newDaemonLogWriter(path, 64, 2, func(*os.File) error { return nil })
	if err != nil {
		t.Fatalf("newDaemonLogWriter error: %v", err)
	}
	defer writer.Close()

	for i := 0; i < 8; i++ {
		if _, err := writer.Write([]byte(strings.Repeat("x", 24) + "\n")); err != nil {
			t.Fatalf("Write error: %v", err)
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated backup missing: %v", err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected backup beyond limit: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("active log missing: %v", err)
	}
	if info.Size() > 64 {
		t.Fatalf("active log size=%d, want <=64", info.Size())
	}
}

func TestDaemonLogWriterTightensExistingFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weclaw.log")
	if err := os.WriteFile(path, []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	writer, err := newDaemonLogWriter(path, 0, 0, nil)
	if err != nil {
		t.Fatalf("newDaemonLogWriter error: %v", err)
	}
	defer writer.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%o, want 600", got)
	}
}

func TestDaemonLogWriterRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.log")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "weclaw.log")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if writer, err := newDaemonLogWriter(path, 0, 0, nil); err == nil {
		_ = writer.Close()
		t.Fatal("newDaemonLogWriter accepted symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original\n" {
		t.Fatalf("symlink target changed: %q", data)
	}
}
