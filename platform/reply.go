package platform

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

// ClientIDSetter 允许消息层把当前入站消息的稳定客户端 ID 交给平台 adapter。
// adapter 可用它为首条回复提供幂等键；不支持的平台无需实现。
type ClientIDSetter interface {
	SetClientID(clientID string)
}

// TextChunkLimitSetter 允许消息层按当前 Agent 上下文调整长文本分片上限。
type TextChunkLimitSetter interface {
	SetTextChunkLimit(maxRunes int)
}

// RemoteMediaSender 允许平台 adapter 直接下载并发送远程媒体。
type RemoteMediaSender interface {
	SendMediaFromURL(ctx context.Context, mediaURL string) error
}

// TaskCardReporter 是平台可选能力，用于把后续交互绑定到当前任务卡片。
type TaskCardReporter interface {
	CurrentTaskCardID() string
}

// TaskCardBinder 允许长任务在展示卡重锚后，把后续审批和问答指向新卡。
type TaskCardBinder interface {
	BindTaskCard(cardID string)
}

// ProgressReplierProvider 允许临时交互包装器提供独立的进度卡回复器。
// 典型场景是卡片回调：命令结果仍原地更新，运行任务则另发到消息底部。
type ProgressReplierProvider interface {
	ProgressReplier() Replier
}

// DeliveryRoute 是跨进程恢复终态投递所需的最小平台路由。
// 它不包含微信 context_token、飞书凭据或任何 Agent 认证信息。
type DeliveryRoute struct {
	Platform  PlatformName `json:"platform"`
	AccountID string       `json:"account_id,omitempty"`
	ChatID    string       `json:"chat_id"`
	ReplyToID string       `json:"reply_to_id,omitempty"`
}

func (r DeliveryRoute) Valid() bool {
	return strings.TrimSpace(string(r.Platform)) != "" && strings.TrimSpace(r.ChatID) != ""
}

// DeliveryRouteReporter 允许消息层把终态绑定到可重建的平台路由。
type DeliveryRouteReporter interface {
	DeliveryRoute() DeliveryRoute
}

// IdempotentTextReplier 使用稳定 delivery key 发送文本；重试同一 key 不应产生重复消息。
type IdempotentTextReplier interface {
	SendTextIdempotent(ctx context.Context, text string, deliveryKey string) error
}

// TerminalCheckpoint 是 adapter 自描述、可持久化的终态更新操作。
type TerminalCheckpoint struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// DurableStreamReference 是可跨进程恢复同一张流式卡片的 adapter 自描述引用。
// 引用只保存平台卡片定位和单调序列，不包含平台凭据。
type DurableStreamReference struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// DurableStreamReferenceExporter 在进程退出前导出仍处于进行态的卡片引用。
type DurableStreamReferenceExporter interface {
	DurableReference() (DurableStreamReference, error)
}

// DurableStreamTerminalPreparer 在新进程中根据持久化引用生成终态操作。
type DurableStreamTerminalPreparer interface {
	PrepareTerminalFromReference(reference DurableStreamReference, finalContent string, failed bool) (TerminalCheckpoint, error)
}

// DurableTerminalStream 在执行网络写入前冻结并导出终态操作。
type DurableTerminalStream interface {
	PrepareTerminal(finalContent string, failed bool) (TerminalCheckpoint, error)
}

// DurableTerminalReplier 用重建后的平台客户端重放持久化终态操作。
type DurableTerminalReplier interface {
	DeliverTerminal(ctx context.Context, checkpoint TerminalCheckpoint) error
}

// OutboundReplierFactory 表示平台可为主动发送 API 创建会话回复器。
type OutboundReplierFactory interface {
	NewReplier(chatID string) Replier
}

// OutboundRouteReplierFactory 在恢复投递时保留原消息 / 话题回复关系。
type OutboundRouteReplierFactory interface {
	NewReplierForRoute(route DeliveryRoute) Replier
}

// Stream 表示一次流式回复会话，adapter 负责平台状态机与节流。
type Stream interface {
	Update(ctx context.Context, content string) error
	Complete(ctx context.Context, finalContent string) error
	Fail(ctx context.Context, errText string) error
}

// SupersedableStream 是流的可选能力，用于停止旧展示位置但不宣告任务终态。
type SupersedableStream interface {
	Supersede(ctx context.Context, notice string) error
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
