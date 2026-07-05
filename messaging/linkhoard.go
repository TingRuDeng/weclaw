package messaging

import (
	"regexp"
	"strings"
)

var reURL = regexp.MustCompile(`https?://\S+`)

const maxLinkMetadataBytes = 5 * 1024 * 1024

// IsURL checks if the text is (or starts with) a URL.
func IsURL(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")
}

// ExtractURL extracts the first URL from text.
func ExtractURL(text string) string {
	match := reURL.FindString(text)
	return match
}

// LinkMetadata holds extracted metadata from a web page.
type LinkMetadata struct {
	Title       string
	Description string
	Author      string
	OGImage     string
	Published   string
	Body        string
}
