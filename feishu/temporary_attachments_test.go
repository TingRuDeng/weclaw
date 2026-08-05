package feishu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFeishuResourceFileSecuresDirectoryAndFileBeforeUse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}

	path, size, err := writeFeishuResourceFile(dir, strings.NewReader("secret attachment"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if size != int64(len("secret attachment")) {
		t.Fatalf("size=%d", size)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir info=%v err=%v, want real 0700 directory", dirInfo, err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("file info=%v err=%v, want regular 0600 file", info, err)
	}
}

func TestWriteFeishuResourceFileRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(root, "attachments")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatal(err)
	}

	if _, _, err := writeFeishuResourceFile(symlinkDir, strings.NewReader("data")); err == nil {
		t.Fatal("symlink attachment directory should be rejected")
	}
	entries, err := os.ReadDir(realDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink target entries=%v err=%v, want untouched", entries, err)
	}
}
