package messaging

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeFileNameTruncatesAtUTF8Boundary(t *testing.T) {
	name := strings.Repeat("中", 100)
	got := sanitizeFileName(name)
	if !utf8.ValidString(got) {
		t.Fatalf("文件名不是合法 UTF-8：%q", got)
	}
	if len(got) > maxLinkhoardBaseNameBytes {
		t.Fatalf("文件名字节数=%d，超过预算 %d", len(got), maxLinkhoardBaseNameBytes)
	}
}
