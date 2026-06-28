package feishu

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)
var repeatedNewlinePattern = regexp.MustCompile(`\n{2,}`)

// resourceDownloader 抽象飞书资源下载，便于单元测试替换 SDK 调用。
type resourceDownloader interface {
	DownloadResource(ctx context.Context, messageID string, resource types.Resource) (platform.Attachment, error)
}

type sdkResourceDownloader struct {
	client *lark.Client
}

// newSDKResourceDownloader 创建基于飞书 REST client 的资源下载器。
func newSDKResourceDownloader(client *lark.Client) resourceDownloader {
	return &sdkResourceDownloader{client: client}
}

// DownloadResource 下载飞书消息资源并转换为统一附件模型。
func (d *sdkResourceDownloader) DownloadResource(ctx context.Context, messageID string, resource types.Resource) (platform.Attachment, error) {
	resourceType := feishuResourceType(resource.Type)
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(resource.FileKey).
		Type(resourceType).
		Build()
	resp, err := d.client.Im.MessageResource.Get(ctx, req)
	if err != nil {
		return platform.Attachment{}, err
	}
	if !resp.Success() {
		return platform.Attachment{}, fmt.Errorf("download feishu resource %s failed: code=%d msg=%s", resource.FileKey, resp.Code, resp.Msg)
	}
	target := feishuResourceTarget(resource)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return platform.Attachment{}, err
	}
	if err := resp.WriteFile(target); err != nil {
		return platform.Attachment{}, err
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return platform.Attachment{}, err
	}
	return platform.Attachment{
		Kind:     attachmentKindForResource(resource.Type),
		Path:     target,
		FileName: firstNonEmpty(resource.FileName, resp.FileName, filepath.Base(target)),
		SourceID: resource.FileKey,
		Metadata: map[string]string{"resource_type": resource.Type},
	}, nil
}

// toIncomingFromMessage 将飞书 P2 消息事件转换为平台统一入站消息。
func (a *Adapter) toIncomingFromMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) (platform.IncomingMessage, bool) {
	normalized := normalize.ParseMessage(event)
	if normalized == nil || normalized.UserID == "" || normalized.MessageID == "" {
		return platform.IncomingMessage{}, false
	}
	if normalized.ChatType != "" && normalized.ChatType != "p2p" {
		return platform.IncomingMessage{}, false
	}
	text := cleanFeishuText(normalized.Content)
	if normalized.RawContentType == "image" || normalized.RawContentType == "file" || normalized.RawContentType == "audio" || normalized.RawContentType == "media" {
		text = ""
	}
	incoming := platform.IncomingMessage{
		Platform:     platform.PlatformFeishu,
		AccountID:    a.creds.AppID,
		UserID:       normalized.UserID,
		ChatID:       normalized.ChatID,
		MessageID:    normalized.MessageID,
		ReplyToID:    normalized.MessageID,
		ContextToken: normalized.MessageID,
		Text:         text,
		Metadata: map[string]string{
			"raw_content_type": normalized.RawContentType,
		},
	}
	for _, resource := range normalized.Resources {
		attachment, err := a.downloader.DownloadResource(ctx, normalized.MessageID, resource)
		if err != nil {
			return platform.IncomingMessage{}, false
		}
		incoming.Attachments = append(incoming.Attachments, attachment)
	}
	if strings.TrimSpace(incoming.Text) == "" && len(incoming.Attachments) == 0 {
		return platform.IncomingMessage{}, false
	}
	return incoming, true
}

// cleanFeishuText 清理飞书文本中的轻量 HTML 标记，同时保留普通空格。
func cleanFeishuText(text string) string {
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "<br>", "\n")
	text = strings.ReplaceAll(text, "<br/>", "\n")
	text = strings.ReplaceAll(text, "<br />", "\n")
	text = strings.ReplaceAll(text, "</p>", "\n")
	text = strings.ReplaceAll(text, "<p>", "")
	text = html.UnescapeString(text)
	text = htmlTagPattern.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	text = strings.TrimSpace(strings.Join(lines, "\n"))
	return repeatedNewlinePattern.ReplaceAllString(text, "\n")
}

// feishuResourceType 将 SDK normalize 资源类型映射为消息资源下载接口参数。
func feishuResourceType(resourceType string) string {
	if resourceType == "image" {
		return "image"
	}
	return "file"
}

// attachmentKindForResource 将飞书资源类型映射为统一附件类型。
func attachmentKindForResource(resourceType string) platform.AttachmentKind {
	switch resourceType {
	case "image":
		return platform.AttachmentImage
	case "audio":
		return platform.AttachmentAudio
	case "video", "media":
		return platform.AttachmentVideo
	default:
		return platform.AttachmentFile
	}
}

// feishuResourceTarget 生成本地资源落盘路径，避免使用原始 key 以外的非可信路径。
func feishuResourceTarget(resource types.Resource) string {
	name := sanitizeFilePart(firstNonEmpty(resource.FileName, resource.FileKey))
	if name == "" {
		name = fmt.Sprintf("resource-%d", time.Now().UnixNano())
	}
	return filepath.Join(os.TempDir(), "weclaw-feishu", name)
}

// sanitizeFilePart 清理文件名中的路径分隔符，避免平台资源 key 影响本地路径。
func sanitizeFilePart(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
