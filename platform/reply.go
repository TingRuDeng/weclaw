package platform

import (
	"context"
	"errors"
)

var ErrUnsupported = errors.New("platform capability unsupported")

// Replier 封装当前入站消息所在会话的回复能力。
type Replier interface {
	Capabilities() Capabilities
	SendText(ctx context.Context, text string) error
	SendImage(ctx context.Context, localPath string) error
	SendFile(ctx context.Context, localPath string) error
	Typing(ctx context.Context, on bool) error
	OpenStream(ctx context.Context, opts StreamOptions) (Stream, error)
	AskChoices(ctx context.Context, prompt string, choices []Choice) error
}

// TaskCardReporter 是平台可选能力，用于把后续交互绑定到当前任务卡片。
type TaskCardReporter interface {
	CurrentTaskCardID() string
}

// OutboundReplierFactory 表示平台可为主动发送 API 创建会话回复器。
type OutboundReplierFactory interface {
	NewReplier(chatID string) Replier
}

// Stream 表示一次流式回复会话，adapter 负责平台状态机与节流。
type Stream interface {
	Update(ctx context.Context, content string) error
	Complete(ctx context.Context, finalContent string) error
	Fail(ctx context.Context, errText string) error
}

// StreamOptions 描述流式回复的初始化参数。
type StreamOptions struct {
	Title          string
	InitialContent string
}

const (
	// ChoiceMetadataInteractionKind 标识选择卡承载的交互语义，供平台区分授权与普通提问。
	ChoiceMetadataInteractionKind = "interaction_kind"
	ChoiceInteractionApproval     = "approval"
	ChoiceInteractionUserInput    = "user_input"
	// ChoiceMetadataAgentName 保留产生当前交互的 Agent，避免多 Agent 窗口混淆来源。
	ChoiceMetadataAgentName = "agent_name"
	// ChoiceMetadataButtonType 与 ChoiceMetadataSection 允许平台区分数据项和导航项。
	ChoiceMetadataButtonType = "button_type"
	ChoiceButtonTypeDefault  = "default"
	ChoiceMetadataSection    = "choice_section"
	ChoiceSectionNavigation  = "navigation"
	// ChoiceMetadataNavigationSnapshot 将分页按钮绑定到服务端短期快照。
	ChoiceMetadataNavigationSnapshot = "navigation_snapshot"
)

// Choice 表示一项可由用户选择的编号选项。
type Choice struct {
	ID       string
	Label    string
	Metadata map[string]string
}
