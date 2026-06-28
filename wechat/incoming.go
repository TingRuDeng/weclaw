package wechat

import (
	"strconv"
	"strings"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
)

// ShouldDispatchWeixinMessage 判断微信入站消息是否应进入业务层，过滤自身回显和非完成态消息。
func ShouldDispatchWeixinMessage(botID string, msg ilink.WeixinMessage) bool {
	if isWeClawEcho(botID, msg) {
		return false
	}
	return msg.MessageType == ilink.MessageTypeUser && msg.MessageState == ilink.MessageStateFinish
}

func isWeClawEcho(botID string, msg ilink.WeixinMessage) bool {
	if strings.HasPrefix(strings.TrimSpace(msg.ClientID), weclawClientIDPrefix) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(weixinText(msg)))
	if msg.MessageType != ilink.MessageTypeBot && strings.TrimSpace(msg.FromUserID) != strings.TrimSpace(botID) {
		return false
	}
	return strings.HasPrefix(text, "weclaw:") ||
		strings.HasPrefix(text, "[weclaw]") ||
		strings.HasPrefix(text, "weclaw ")
}

// IncomingFromWeixin 将 iLink 消息转换成平台无关消息。
func IncomingFromWeixin(msg ilink.WeixinMessage) platform.IncomingMessage {
	return platform.IncomingMessage{
		Platform:     platform.PlatformWeChat,
		AccountID:    strings.TrimSpace(msg.ToUserID),
		UserID:       strings.TrimSpace(msg.FromUserID),
		ChatID:       strings.TrimSpace(msg.FromUserID),
		MessageID:    strconv.FormatInt(msg.MessageID, 10),
		Text:         weixinText(msg),
		Attachments:  weixinAttachments(msg),
		ContextToken: msg.ContextToken,
	}
}

func weixinText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil {
			return item.VoiceItem.Text
		}
	}
	return ""
}

func weixinAttachments(msg ilink.WeixinMessage) []platform.Attachment {
	attachments := make([]platform.Attachment, 0, len(msg.ItemList))
	for _, item := range msg.ItemList {
		switch item.Type {
		case ilink.ItemTypeImage:
			if item.ImageItem != nil {
				attachments = append(attachments, weixinImageAttachment(item.ImageItem))
			}
		case ilink.ItemTypeFile:
			if item.FileItem != nil {
				attachments = append(attachments, weixinFileAttachment(item.FileItem))
			}
		case ilink.ItemTypeVoice:
			if item.VoiceItem != nil && item.VoiceItem.Media != nil {
				attachments = append(attachments, platform.Attachment{Kind: platform.AttachmentAudio})
			}
		case ilink.ItemTypeVideo:
			if item.VideoItem != nil && item.VideoItem.Media != nil {
				attachments = append(attachments, platform.Attachment{Kind: platform.AttachmentVideo})
			}
		}
	}
	return attachments
}

func weixinImageAttachment(img *ilink.ImageItem) platform.Attachment {
	attachment := platform.Attachment{Kind: platform.AttachmentImage}
	if img.URL != "" {
		attachment.SourceID = img.URL
	}
	if img.Media != nil {
		attachment.Metadata = map[string]string{
			"encrypt_query_param": img.Media.EncryptQueryParam,
			"aes_key":             img.Media.AESKey,
		}
	}
	return attachment
}

func weixinFileAttachment(file *ilink.FileItem) platform.Attachment {
	attachment := platform.Attachment{
		Kind:      platform.AttachmentFile,
		FileName:  file.FileName,
		SizeBytes: parseWeixinFileSize(file.Len),
	}
	if file.Media != nil {
		attachment.Metadata = map[string]string{
			"encrypt_query_param": file.Media.EncryptQueryParam,
			"aes_key":             file.Media.AESKey,
		}
	}
	return attachment
}

func parseWeixinFileSize(value string) int64 {
	size, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return size
}
