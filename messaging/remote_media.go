package messaging

import (
	"context"
	"net/http"

	"github.com/fastclaw-ai/weclaw/internal/remotefetch"
)

// downloadFile 使用共享远程下载器获取消息附件，保留 messaging 的内容类型推断逻辑。
func downloadFile(ctx context.Context, rawURL string) ([]byte, string, error) {
	return remotefetch.Download(ctx, rawURL, remotefetch.DefaultOptions(), inferContentType)
}

// newRemoteMediaHTTPClient 暴露共享 HTTP client，供历史单测继续覆盖跳转校验。
func newRemoteMediaHTTPClient() *http.Client {
	return remotefetch.NewHTTPClient(remotefetch.DefaultOptions())
}

// validateRemoteMediaURL 兼容 messaging 原有测试入口，实际校验由共享包完成。
func validateRemoteMediaURL(rawURL string) error {
	return remotefetch.ValidateURL(rawURL)
}

// readRemoteMediaBody 兼容 messaging 原有测试入口，统一复用大小限制逻辑。
func readRemoteMediaBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	return remotefetch.ReadBody(resp, maxBytes)
}
