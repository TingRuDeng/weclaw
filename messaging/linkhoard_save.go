package messaging

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maxLinkhoardBaseNameBytes = 200

// sanitizeFileName 清理文件名并在 UTF-8 字符边界内控制字节预算。
func sanitizeFileName(name string) string {
	replacer := strings.NewReplacer(
		"/", "", "\\", "", ":", "", "*", "",
		"?", "", "\"", "", "<", "", ">", "", "|", "",
	)
	result := replacer.Replace(name)
	result = strings.TrimSpace(result)
	result = truncateUTF8Bytes(result, maxLinkhoardBaseNameBytes)
	if result == "" {
		result = "untitled"
	}
	return result
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	used := 0
	for index, current := range value {
		size := utf8.RuneLen(current)
		if used+size > maxBytes {
			return value[:index]
		}
		used += size
	}
	return value
}

// isWeChatURL checks if a URL is a WeChat article.
func isWeChatURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "mp.weixin.qq.com" || host == "weixin.qq.com"
}

// SaveLinkToLinkhoard fetches a URL and saves it as a Linkhoard-compatible markdown file.
// WeChat articles use direct fetch with browser headers; other sites use Jina Reader.
func SaveLinkToLinkhoard(ctx context.Context, saveDir, rawURL string) (string, error) {
	var meta *LinkMetadata
	var err error

	if isWeChatURL(rawURL) {
		meta, err = FetchLinkMetadata(ctx, rawURL)
	} else {
		meta, err = FetchViaJina(ctx, rawURL)
		if err != nil {
			// Fallback to direct fetch
			log.Printf("[linkhoard] Jina failed (%v), falling back to direct fetch", err)
			meta, err = FetchLinkMetadata(ctx, rawURL)
		}
	}
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}

	// Ensure save directory exists
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}

	// Build frontmatter
	title := sanitizeFileName(meta.Title)
	created := time.Now().UTC().Format(time.RFC3339)
	itemID := uuid.New().String()

	// Normalize body text
	body := strings.TrimSpace(meta.Body)
	// Collapse excessive newlines
	for strings.Contains(body, "\n\n\n") {
		body = strings.ReplaceAll(body, "\n\n\n", "\n\n")
	}

	// Build author field
	authorField := "author: []\n"
	if meta.Author != "" {
		authorField = fmt.Sprintf("author:\n  - '[[%s]]'\n", meta.Author)
	}

	// Build markdown content
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: '%s'\n", strings.ReplaceAll(meta.Title, "'", "''")))
	sb.WriteString(fmt.Sprintf("source: '%s'\n", rawURL))
	sb.WriteString(fmt.Sprintf("published: '%s'\n", meta.Published))
	sb.WriteString(fmt.Sprintf("created: '%s'\n", created))
	sb.WriteString(fmt.Sprintf("description: '%s'\n", strings.ReplaceAll(meta.Description, "'", "''")))
	if meta.OGImage != "" {
		sb.WriteString(fmt.Sprintf("openGraphImage: '%s'\n", meta.OGImage))
	}
	sb.WriteString(authorField)
	sb.WriteString("---\n\n")
	if body != "" {
		sb.WriteString(body)
		sb.WriteString("\n")
	}

	sidecarContent := fmt.Sprintf("---\nid: %s\n---\n", itemID)
	filePath, err := writeUniqueArtifactPair(
		saveDir,
		title,
		".md",
		[]byte(sb.String()),
		[]byte(sidecarContent),
	)
	if err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	log.Printf("[linkhoard] saved %q to %s", meta.Title, filePath)
	return meta.Title, nil
}
