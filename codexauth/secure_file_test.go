package codexauth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteSecureFileKeepsCommittedRenameWhenDirectorySyncFails(t *testing.T) {
	originalSync := syncSecureFileDirectory
	syncSecureFileDirectory = func(string) error {
		return errors.New("injected directory sync failure")
	}
	t.Cleanup(func() {
		syncSecureFileDirectory = originalSync
	})

	path := filepath.Join(t.TempDir(), "secure", "state.json")
	if err := atomicWriteSecureFile(path, []byte("committed")); err != nil {
		t.Fatalf("atomicWriteSecureFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed file: %v", err)
	}
	if string(data) != "committed" {
		t.Fatalf("file content=%q, want committed", data)
	}
}
