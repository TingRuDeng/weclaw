package platform

import "strings"

// IncomingMessage 是业务层接收的跨平台统一消息模型。
type IncomingMessage struct {
	Platform     PlatformName
	AccountID    string
	UserID       string
	UserAliases  []string
	ChatID       string
	MessageID    string
	Text         string
	Attachments  []Attachment
	RawCommand   *CardAction
	ReplyToID    string
	ContextToken string
	Metadata     map[string]string
}

// UserIdentityKeys 返回可用于访问控制的用户身份，保留 UserID 作为主身份。
func (m IncomingMessage) UserIdentityKeys() []string {
	values := make([]string, 0, len(m.UserAliases)+1)
	seen := make(map[string]bool, len(m.UserAliases)+1)
	addIdentityKey(&values, seen, m.UserID)
	for _, alias := range m.UserAliases {
		addIdentityKey(&values, seen, alias)
	}
	return values
}

// addIdentityKey 去重追加非空身份，避免同一用户重复匹配。
func addIdentityKey(values *[]string, seen map[string]bool, value string) {
	value = strings.TrimSpace(value)
	if value == "" || seen[value] {
		return
	}
	seen[value] = true
	*values = append(*values, value)
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
