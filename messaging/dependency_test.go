package messaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMessagingProductionCodeDoesNotImportPlatformAdapters(t *testing.T) {
	for _, file := range productionGoFiles(t, ".") {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(data)
		for _, forbidden := range []string{
			`github.com/fastclaw-ai/weclaw/ilink`,
			`github.com/fastclaw-ai/weclaw/feishu`,
			`github.com/fastclaw-ai/weclaw/lark`,
			`github.com/fastclaw-ai/weclaw/wechat`,
			`github.com/larksuite/oapi-sdk-go`,
		} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s imports forbidden adapter dependency %s", file, forbidden)
			}
		}
	}
}

func productionGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}
