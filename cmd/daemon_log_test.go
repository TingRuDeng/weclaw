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
