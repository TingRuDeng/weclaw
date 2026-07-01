package platform

import "strings"

// IncomingMessage 是业务层接收的跨平台统一消息模型。
type IncomingMessage struct {
	Platform     PlatformName
	AccountID    string
	UserID       string
	ChatID       string
	MessageID    string
	Text         string
	Attachments  []Attachment
	RawCommand   *CardAction
	ReplyToID    string
	ContextToken string
	Metadata     map[string]string
}

// CardActionResult 表示卡片动作进入业务层后的处理结果。
type CardActionResult string

const (
	CardActionResultConsumed CardActionResult = "consumed"
	CardActionResultExpired  CardActionResult = "expired"
)

// ConversationKey 返回跨平台会话隔离 key，避免不同平台的相同用户 ID 串话。
func (m IncomingMessage) ConversationKey() string {
	platformName := strings.TrimSpace(string(m.Platform))
	userID := strings.TrimSpace(m.UserID)
	if platformName == "" {
		return userID
	}
	return platformName + ":" + userID
}

// AttachmentKind 标识附件类型，由 adapter 负责从平台原始资源转换。
type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentFile  AttachmentKind = "file"
	AttachmentAudio AttachmentKind = "audio"
	AttachmentVideo AttachmentKind = "video"
)

// Attachment 描述已下载或可读取的本地附件。
type Attachment struct {
	Kind        AttachmentKind
	Path        string
	FileName    string
	ContentType string
	SizeBytes   int64
	SourceID    string
	Metadata    map[string]string
}

// CardAction 表示平台卡片按钮等交互动作。
type CardAction struct {
	Action string
	Value  map[string]string
	Result chan<- CardActionResult
}
