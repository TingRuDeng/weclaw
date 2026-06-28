package wechat

import (
	"context"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/ilink"
)

var uploadFileToCDN = UploadFileToCDN

// SendMediaFromURL 下载远程媒体后通过微信 CDN 发送，URL 安全校验在下载前完成。
func (r *Replier) SendMediaFromURL(ctx context.Context, mediaURL string) error {
	data, contentType, err := downloadFile(ctx, mediaURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", mediaURL, err)
	}
	return r.sendMediaData(ctx, filenameFromURL(mediaURL), mediaURL, data, contentType)
}

func (r *Replier) sendMediaFromPath(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return r.sendMediaData(ctx, filepath.Base(path), path, data, inferContentType(path))
}

func (r *Replier) sendMediaData(ctx context.Context, fileName string, source string, data []byte, contentType string) error {
	if fileName == "" {
		fileName = "file"
	}
	cdnMediaType, itemType := classifyMedia(contentType, source)
	log.Printf("[wechat] uploading %s (%s, %d bytes) for %s", source, contentType, len(data), r.ToUserID)
	uploaded, err := uploadFileToCDN(ctx, r.Client, data, r.ToUserID, cdnMediaType)
	if err != nil {
		return fmt.Errorf("upload to CDN: %w", err)
	}
	media := &ilink.MediaInfo{
		EncryptQueryParam: uploaded.DownloadParam,
		AESKey:            AESKeyToBase64(uploaded.AESKeyHex),
		EncryptType:       1,
	}
	item := mediaMessageItem(itemType, fileName, uploaded, media)
	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   r.Client.BotID(),
			ToUserID:     r.ToUserID,
			ClientID:     NewClientID(),
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList:     []ilink.MessageItem{item},
			ContextToken: r.ContextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}
	resp, err := r.Client.SendMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("send media message: %w", err)
	}
	if resp.Ret != 0 {
		return fmt.Errorf("send media failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
	}
	return nil
}

func mediaMessageItem(itemType int, fileName string, uploaded *UploadedFile, media *ilink.MediaInfo) ilink.MessageItem {
	switch itemType {
	case ilink.ItemTypeImage:
		return ilink.MessageItem{
			Type:      ilink.ItemTypeImage,
			ImageItem: &ilink.ImageItem{Media: media, MidSize: uploaded.CipherSize},
		}
	case ilink.ItemTypeVideo:
		return ilink.MessageItem{
			Type:      ilink.ItemTypeVideo,
			VideoItem: &ilink.VideoItem{Media: media, VideoSize: uploaded.CipherSize},
		}
	default:
		return ilink.MessageItem{
			Type:     ilink.ItemTypeFile,
			FileItem: &ilink.FileItem{Media: media, FileName: fileName, Len: fmt.Sprintf("%d", uploaded.FileSize)},
		}
	}
}

func classifyMedia(contentType string, source string) (int, int) {
	ct := strings.ToLower(contentType)
	if strings.HasPrefix(ct, "image/") || isImageExt(source) {
		return ilink.CDNMediaTypeImage, ilink.ItemTypeImage
	}
	if strings.HasPrefix(ct, "video/") || isVideoExt(source) {
		return ilink.CDNMediaTypeVideo, ilink.ItemTypeVideo
	}
	return ilink.CDNMediaTypeFile, ilink.ItemTypeFile
}

func inferContentType(path string) string {
	ext := filepath.Ext(stripQuery(path))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func isImageExt(path string) bool {
	switch strings.ToLower(filepath.Ext(stripQuery(path))) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	default:
		return false
	}
}

func isVideoExt(path string) bool {
	switch strings.ToLower(filepath.Ext(stripQuery(path))) {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi":
		return true
	default:
		return false
	}
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
