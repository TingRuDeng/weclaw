package wechat

import (
	"strings"
	"testing"
)

func TestMarkdownToPlainTextPreservesCodeLiteral(t *testing.T) {
	input := "```go\n2**3**4\npath /a/b\n```"
	got := MarkdownToPlainText(input)
	if !strings.Contains(got, "2**3**4") {
		t.Fatalf("code formatting was changed: %q", got)
	}
}

func TestMarkdownToPlainTextKeepsLinkAndImageContext(t *testing.T) {
	got := MarkdownToPlainText("[文档](https://example.com/doc) ![截图](https://example.com/image.png)")
	if !strings.Contains(got, "文档 (https://example.com/doc)") {
		t.Fatalf("link target was dropped: %q", got)
	}
	if !strings.Contains(got, "[图片: 截图]") {
		t.Fatalf("image context was dropped: %q", got)
	}
}
