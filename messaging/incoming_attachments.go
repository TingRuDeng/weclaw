package messaging

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
	"github.com/google/uuid"
)

const maxIncomingAttachmentBytes int64 = 32 * 1024 * 1024

type savedIncomingFile struct {
	name string
	path string
}

func firstAttachment(attachments []platform.Attachment, kind platform.AttachmentKind) (platform.Attachment, bool) {
	for _, attachment := range attachments {
		if attachment.Kind == kind {
			return attachment, true
		}
	}
	return platform.Attachment{}, false
}

func (h *Handler) handleFileAttachment(ctx context.Context, userID string, reply platform.Replier, file platform.Attachment, text string) (string, bool) {
	saved, err := h.saveIncomingAttachment(ctx, file)
	if err != nil {
		log.Printf("[handler] failed to save incoming file from %s: %v", userID, err)
		sendPlatformText(ctx, reply, userID, fmt.Sprintf("文件保存失败：%v", err))
		return "", false
	}
	log.Printf("[handler] saved incoming file from %s: %s", userID, saved.path)
	return buildFileAgentMessage(text, saved), true
}

func (h *Handler) handleImageAttachment(ctx context.Context, userID string, reply platform.Replier, image platform.Attachment, text string) (string, bool) {
	saved, err := h.saveIncomingAttachment(ctx, image)
	if err != nil {
		log.Printf("[handler] failed to save incoming image from %s: %v", userID, err)
		sendPlatformText(ctx, reply, userID, fmt.Sprintf("图片保存失败：%v", err))
		return "", false
	}
	log.Printf("[handler] saved incoming image from %s: %s", userID, saved.path)
	return buildImageAgentMessage(text, saved), true
}

func (h *Handler) saveIncomingAttachment(ctx context.Context, file platform.Attachment) (savedIncomingFile, error) {
	if file.Metadata["temporary"] == "true" && file.Path != "" {
		defer os.Remove(file.Path)
	}
	data, err := h.readAttachmentData(ctx, file)
	if err != nil {
		return savedIncomingFile{}, err
	}
	fileName := safeIncomingFileName(file.FileName)
	dir := h.incomingFileDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return savedIncomingFile{}, fmt.Errorf("创建保存目录失败：%w", err)
	}
	path, err := writeIncomingAttachment(dir, fileName, data)
	if err != nil {
		return savedIncomingFile{}, err
	}
	return savedIncomingFile{name: fileName, path: path}, nil
}

func (h *Handler) readAttachmentData(ctx context.Context, attachment platform.Attachment) ([]byte, error) {
	if attachment.Path != "" {
		return readLocalAttachment(attachment.Path)
	}
	encryptQueryParam := attachment.Metadata["encrypt_query_param"]
	aesKey := attachment.Metadata["aes_key"]
	if encryptQueryParam == "" || aesKey == "" {
		return nil, fmt.Errorf("文件缺少下载信息")
	}
	downloader := h.cdnDownloader
	if downloader == nil {
		downloader = wechat.DownloadFileFromCDN
	}
	data, err := downloader(ctx, encryptQueryParam, aesKey)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxIncomingAttachmentBytes {
		return nil, fmt.Errorf("文件超过 %d MiB 限制", maxIncomingAttachmentBytes/(1024*1024))
	}
	return data, nil
}

func readLocalAttachment(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("附件不是普通文件")
	}
	if info.Size() > maxIncomingAttachmentBytes {
		return nil, fmt.Errorf("文件超过 %d MiB 限制", maxIncomingAttachmentBytes/(1024*1024))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxIncomingAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxIncomingAttachmentBytes {
		return nil, fmt.Errorf("文件超过 %d MiB 限制", maxIncomingAttachmentBytes/(1024*1024))
	}
	return data, nil
}

func writeIncomingAttachment(dir string, fileName string, data []byte) (string, error) {
	ext := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)
	pattern := time.Now().Format("20060102-150405") + "-" + base + "-*" + ext
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("创建文件失败：%w", err)
	}
	path := file.Name()
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("写入文件失败：%w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("关闭文件失败：%w", err)
	}
	return path, nil
}

func (h *Handler) incomingFileDir() string {
	if strings.TrimSpace(h.saveDir) != "" {
		return h.saveDir
	}
	return defaultAttachmentWorkspace()
}

func safeIncomingFileName(fileName string) string {
	fileName = filepath.Base(strings.TrimSpace(fileName))
	if fileName == "." || fileName == string(filepath.Separator) || fileName == "" {
		return "wechat-file"
	}
	return fileName
}

func buildFileAgentMessage(userText string, file savedIncomingFile) string {
	userText = strings.TrimSpace(userText)
	fileInfo := "用户发送了一个文件，请查看并分析：\n文件名：" + file.name + "\n本地路径：" + file.path
	if userText == "" {
		return fileInfo
	}
	return userText + "\n\n" + fileInfo
}

func buildImageAgentMessage(userText string, file savedIncomingFile) string {
	userText = strings.TrimSpace(userText)
	imageInfo := "用户发送了一张图片，请查看并分析：\n文件名：" + file.name + "\n本地路径：" + file.path
	if userText == "" {
		return imageInfo
	}
	return userText + "\n\n" + imageInfo
}

func (h *Handler) handleImageAttachmentSave(ctx context.Context, userID string, reply platform.Replier, img platform.Attachment) {
	log.Printf("[handler] received image from %s, saving to %s", userID, h.saveDir)
	var data []byte
	var err error
	if img.SourceID != "" {
		data, _, err = downloadFile(ctx, img.SourceID)
	} else {
		data, err = h.readAttachmentData(ctx, img)
	}
	if err != nil {
		log.Printf("[handler] failed to download image from %s: %v", userID, err)
		sendPlatformText(ctx, reply, userID, fmt.Sprintf("Failed to save image: %v", err))
		return
	}
	ext := detectImageExt(data)
	if err := os.MkdirAll(h.saveDir, 0o755); err != nil {
		log.Printf("[handler] failed to create save dir: %v", err)
		return
	}
	sidecarContent := fmt.Sprintf("---\nid: %s\n---\n", uuid.New().String())
	filePath, err := writeUniqueArtifactPair(
		h.saveDir,
		time.Now().Format("20060102-150405"),
		ext,
		data,
		[]byte(sidecarContent),
	)
	if err != nil {
		log.Printf("[handler] failed to write image: %v", err)
		sendPlatformText(ctx, reply, userID, fmt.Sprintf("Failed to save image: %v", err))
		return
	}
	sendPlatformText(ctx, reply, userID, fmt.Sprintf("Saved image: %s", filePath))
}

func detectImageExt(data []byte) string {
	if len(data) < 4 {
		return ".bin"
	}
	// PNG 文件头：89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return ".png"
	}
	// JPEG 文件头：FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return ".jpg"
	}
	// GIF 文件头：47 49 46
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return ".gif"
	}
	// WebP 文件头：52 49 46 46 ... 57 45 42 50
	if len(data) >= 12 && data[0] == 0x52 && data[1] == 0x49 && data[8] == 0x57 && data[9] == 0x45 {
		return ".webp"
	}
	// BMP 文件头：42 4D
	if data[0] == 0x42 && data[1] == 0x4D {
		return ".bmp"
	}
	return ".jpg"
}
