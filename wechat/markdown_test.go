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

func TestMarkdownToPlainTextPreservesInlineCodeAndIdentifiers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "反引号内的粗体和删除线字面量",
			input: "计算 `2**3**4` 与 `~~raw~~` 和 `/src/__init__.py`",
			want:  "计算 2**3**4 与 ~~raw~~ 和 /src/__init__.py",
		},
		{
			name:  "反引号内的Markdown链接",
			input: "示例 `[a](b)` 不应被展开",
			want:  "示例 [a](b) 不应被展开",
		},
		{
			name:  "裸路径与双下划线标识符不被误伤",
			input: "修改 /src/__init__.py 和 foo__bar__baz 后测试",
			want:  "修改 /src/__init__.py 和 foo__bar__baz 后测试",
		},
		{
			name:  "正常粗体仍转换为纯文本",
			input: "这是 **星号粗体** 与 __下划线粗体__ 文本",
			want:  "这是 星号粗体 与 下划线粗体 文本",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MarkdownToPlainText(tt.input); got != tt.want {
				t.Fatalf("MarkdownToPlainText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
