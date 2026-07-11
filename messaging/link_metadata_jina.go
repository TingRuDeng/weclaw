package messaging

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// FetchViaJina fetches a URL via Jina Reader API and returns metadata + markdown body.
func FetchViaJina(ctx context.Context, rawURL string) (*LinkMetadata, error) {
	if err := validateRemoteMediaURL(rawURL); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	jinaURL := "https://r.jina.ai/" + rawURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain")

	resp, err := newRemoteMediaHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jina HTTP %d", resp.StatusCode)
	}

	data, err := readLinkMetadataBody(resp, maxLinkMetadataBytes)
	if err != nil {
		return nil, err
	}

	meta := &LinkMetadata{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	// Parse Jina header lines: "Title:", "URL Source:", "Published Time:", then "Markdown Content:"
	inBody := false
	var body strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if inBody {
			body.WriteString(line)
			body.WriteString("\n")
			continue
		}
		if strings.HasPrefix(line, "Title: ") {
			meta.Title = strings.TrimPrefix(line, "Title: ")
		} else if strings.HasPrefix(line, "Published Time: ") {
			meta.Published = strings.TrimPrefix(line, "Published Time: ")
		} else if line == "Markdown Content:" {
			inBody = true
		}
	}

	if meta.Title == "" {
		meta.Title = rawURL
	}
	meta.Body = strings.TrimSpace(body.String())

	// Check for Jina failure (CAPTCHA, empty content)
	if meta.Body == "" || strings.Contains(meta.Body, "环境异常") || strings.Contains(meta.Body, "CAPTCHA") {
		return nil, fmt.Errorf("jina returned empty or blocked content")
	}

	return meta, nil
}
