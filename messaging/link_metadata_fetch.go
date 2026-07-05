package messaging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/net/html"
)

// FetchLinkMetadata fetches a URL and extracts metadata from the HTML.
func FetchLinkMetadata(ctx context.Context, rawURL string) (*LinkMetadata, error) {
	if err := validateRemoteMediaURL(rawURL); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://mp.weixin.qq.com/")

	resp, err := newRemoteMediaHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := readLinkMetadataBody(resp, maxLinkMetadataBytes)
	if err != nil {
		return nil, err
	}

	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}

	meta := &LinkMetadata{}
	extractMeta(doc, meta)

	// Fallback title from URL if empty
	if meta.Title == "" {
		meta.Title = rawURL
	}

	return meta, nil
}

// readLinkMetadataBody 限制链接正文大小，避免 HTML 解析前把异常响应完整读入内存。
func readLinkMetadataBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("link metadata is too large: %d > %d", resp.ContentLength, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("link metadata exceeds %d bytes", maxBytes)
	}
	return data, nil
}
