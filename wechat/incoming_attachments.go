package wechat

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
	"github.com/fastclaw-ai/weclaw/platform"
)

const maxWeChatAttachmentBytes int64 = 32 * 1024 * 1024

func (a *Adapter) attachIncomingMedia(ctx context.Context, incoming *platform.IncomingMessage) {
	if a == nil || incoming == nil || len(incoming.Attachments) == 0 {
		return
	}
	download := a.downloadCDN
	if download == nil {
		download = DownloadFileFromCDN
	}
	for index := range incoming.Attachments {
		attachment := &incoming.Attachments[index]
		if strings.TrimSpace(attachment.Path) != "" || attachment.Metadata == nil {
			continue
		}
		encryptQueryParam := strings.TrimSpace(attachment.Metadata["encrypt_query_param"])
		aesKey := strings.TrimSpace(attachment.Metadata["aes_key"])
		if encryptQueryParam == "" || aesKey == "" {
			continue
		}
		delete(attachment.Metadata, "encrypt_query_param")
		delete(attachment.Metadata, "aes_key")
		data, err := download(ctx, encryptQueryParam, aesKey)
		if err != nil {
			log.Printf("[wechat] failed to download incoming CDN attachment: %v", err)
			attachment.Metadata["download_error"] = err.Error()
			continue
		}
		if int64(len(data)) > maxWeChatAttachmentBytes {
			attachment.Metadata["download_error"] = fmt.Sprintf("文件超过 %d MiB 限制", maxWeChatAttachmentBytes/(1024*1024))
			continue
		}
		path, err := writeWeChatAttachmentFile(filepath.Join(os.TempDir(), "weclaw-wechat"), data)
		if err != nil {
			log.Printf("[wechat] failed to stage incoming CDN attachment: %v", err)
			attachment.Metadata["download_error"] = err.Error()
			continue
		}
		attachment.Path = path
		attachment.Metadata["temporary"] = "true"
	}
}

func writeWeChatAttachmentFile(dir string, data []byte) (string, error) {
	if err := securefile.EnsureDir(dir); err != nil {
		return "", fmt.Errorf("secure wechat attachment directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".attachment-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create wechat attachment: %w", err)
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("chmod wechat attachment: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return "", fmt.Errorf("write wechat attachment: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close wechat attachment: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect wechat attachment: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() != int64(len(data)) {
		return "", fmt.Errorf("wechat attachment failed regular-file validation")
	}
	keep = true
	return path, nil
}

func cleanupTemporaryAttachments(attachments []platform.Attachment) {
	for _, attachment := range attachments {
		if attachment.Metadata["temporary"] == "true" && strings.TrimSpace(attachment.Path) != "" {
			_ = os.Remove(attachment.Path)
		}
	}
}
