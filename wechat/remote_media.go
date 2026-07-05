package wechat

import (
	"context"

	"github.com/fastclaw-ai/weclaw/internal/remotefetch"
)

// downloadFile 使用共享远程下载器获取微信附件，保留微信侧的内容类型推断逻辑。
func downloadFile(ctx context.Context, rawURL string) ([]byte, string, error) {
	return remotefetch.Download(ctx, rawURL, remotefetch.DefaultOptions(), inferContentType)
}
