package messaging

import (
	"mime"
	"path/filepath"
	"regexp"
	"strings"
)

var reMarkdownImage = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

// ExtractImageURLs 提取 markdown 图片中的远程 URL，供平台 replier 发送附件。
func ExtractImageURLs(text string) []string {
	matches := reMarkdownImage.FindAllStringSubmatch(text, -1)
	urls := make([]string, 0, len(matches))
	for _, match := range matches {
		url := strings.TrimSpace(match[1])
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			urls = append(urls, url)
		}
	}
	return urls
}

func inferContentType(path string) string {
	ext := filepath.Ext(stripQuery(path))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func stripQuery(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

func filenameFromURL(rawURL string) string {
	name := filepath.Base(stripQuery(rawURL))
	if name == "" || name == "." || name == "/" {
		return "file"
	}
	return name
}
