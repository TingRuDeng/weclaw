package platform

import "context"

// PlatformName 标识一个 IM 平台，必须稳定用于配置和会话隔离。
type PlatformName string

const (
	PlatformWeChat PlatformName = "wechat"
	PlatformFeishu PlatformName = "feishu"
)

// DispatchFunc 是平台 adapter 向业务层分发入站消息的统一入口。
type DispatchFunc func(ctx context.Context, msg IncomingMessage, reply Replier)

// Capabilities 描述平台回复能力，业务层通过 Replier 表达意图，由 adapter 负责降级。
type Capabilities struct {
	Text      bool
	Typing    bool
	Image     bool
	File      bool
	Card      bool
	Streaming bool
	Buttons   bool
	LongText  bool
	// FinalReplyOutsideStream 表示流式更新不会触发新消息提醒，最终结果需要独立发送。
	FinalReplyOutsideStream bool
}

// Platform 表示一个可运行的 IM 接入端。
type Platform interface {
	Name() PlatformName
	AccountID() string
	Capabilities() Capabilities
	Run(ctx context.Context, dispatch DispatchFunc) error
}
