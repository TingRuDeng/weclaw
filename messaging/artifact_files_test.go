package messaging

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteUniqueArtifactPairPreservesExistingFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "资料.md")
	existingSidecar := existing + ".sidecar.md"
	if err := os.WriteFile(existing, []byte("旧正文"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingSidecar, []byte("旧索引"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := writeUniqueArtifactPair(dir, "资料", ".md", []byte("新正文"), []byte("新索引"))
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "资料-2.md") {
		t.Fatalf("path=%q，期望使用唯一后缀", path)
	}
	assertFileContent(t, existing, "旧正文")
	assertFileContent(t, existingSidecar, "旧索引")
	assertFileContent(t, path, "新正文")
	assertFileContent(t, path+".sidecar.md", "新索引")
}

func TestWriteUniqueArtifactPairIsConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	const writers = 12
	paths := make(chan string, writers)
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := writeUniqueArtifactPair(dir, "同名资料", ".md", []byte("正文"), []byte("索引"))
			if err != nil {
				errs <- err
				return
			}
			paths <- path
		}()
	}
	wg.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		t.Fatalf("并发保存失败：%v", err)
	}
	seen := map[string]bool{}
	for path := range paths {
		if seen[path] {
			t.Fatalf("并发保存复用了路径：%s", path)
		}
		seen[path] = true
		assertFileContent(t, path, "正文")
		assertFileContent(t, path+".sidecar.md", "索引")
	}
	if len(seen) != writers {
		t.Fatalf("保存文件数=%d，期望 %d", len(seen), writers)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s 内容=%q，期望 %q", path, data, want)
	}
}
