package messaging

import (
	"path/filepath"
	"testing"
)

// canonicalTestPath 返回测试目录在当前操作系统上的真实路径。
func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(realPath)
}
