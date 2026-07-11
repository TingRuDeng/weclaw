package feishu

import (
	"os"
	"sync"

	"github.com/fastclaw-ai/weclaw/platform"
)

// newTemporaryAttachmentCleanup 返回幂等清理函数，确保延迟分发和取消竞态不会重复处理。
func newTemporaryAttachmentCleanup(attachments []platform.Attachment) func() {
	paths := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.Path == "" || attachment.Metadata["temporary"] != "true" {
			continue
		}
		paths = append(paths, attachment.Path)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, path := range paths {
				_ = os.Remove(path)
			}
		})
	}
}
