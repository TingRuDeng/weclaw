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

func TestBuildLinkhoardDocumentEscapesEverySingleQuotedField(t *testing.T) {
	meta := &LinkMetadata{
		Title:       "title's",
		Published:   "publisher's date",
		Description: "description's",
		OGImage:     "https://example.com/image's.png",
		Author:      "author's",
	}
	document := buildLinkhoardDocument(meta, "https://example.com/source's", "creator's time", "body")
	for _, expected := range []string{
		"title: 'title''s'",
		"source: 'https://example.com/source''s'",
		"published: 'publisher''s date'",
		"created: 'creator''s time'",
		"description: 'description''s'",
		"openGraphImage: 'https://example.com/image''s.png'",
		"- '[[author''s]]'",
	} {
		if !strings.Contains(document, expected) {
			t.Fatalf("document missing %q:\n%s", expected, document)
		}
	}
}
