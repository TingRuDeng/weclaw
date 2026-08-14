package platform

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

var ErrUnsupported = errors.New("platform capability unsupported")
var ErrStreamContentTooLarge = errors.New("stream content exceeds platform card limit")

// TaskStreamThinkingIndicator 是任务流在等待下一段用户可见 Agent 回复时的统一活跃提示。
const TaskStreamThinkingIndicator = "思考中....."

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

// DurableCommandResultReference 是平台自描述、可跨进程恢复的命令结果展示引用。
// 引用只能包含更新既有展示所需的最小定位与样式信息，不得包含平台凭据。
type DurableCommandResultReference struct {
	Kind       string `json:"kind"`
	TargetID   string `json:"target_id"`
	Title      string `json:"title,omitempty"`
	Command    string `json:"command,omitempty"`
	ReadyAfter string `json:"ready_after,omitempty"`
}

func (r DurableCommandResultReference) Valid() bool {
	return strings.TrimSpace(r.Kind) != "" && strings.TrimSpace(r.TargetID) != ""
}

// DurableCommandResultReferenceReporter 导出当前命令结果所在的可恢复展示位置。
type DurableCommandResultReferenceReporter interface {
	DurableCommandResultReference() (DurableCommandResultReference, error)
}

// DurableCommandResultReplier 在后台状态恢复后原地更新既有命令结果展示。
type DurableCommandResultReplier interface {
	DeliverCommandResult(ctx context.Context, reference DurableCommandResultReference, text string) error
}

// IdempotentTextReplier 使用稳定 delivery key 发送文本；重试同一 key 不应产生重复消息。
type IdempotentTextReplier interface {
	SendTextIdempotent(ctx context.Context, text string, deliveryKey string) error
}

// TerminalResult 描述独立于流式进度卡的新终态结果消息。
// Text 保留完整最终回答；Title 由消息层提供 Agent 与工作空间上下文。
type TerminalResult struct {
	Title string
	Text  string
	State StreamTerminalState
}

// IdempotentResultReplier 使用稳定 delivery key 发送平台原生终态结果；
// 同一 key 的重试不得产生重复消息或重复分段。
type IdempotentResultReplier interface {
	SendResultIdempotent(ctx context.Context, result TerminalResult, deliveryKey string) error
}

// TerminalCheckpoint 是 adapter 自描述、可持久化的终态更新操作。
type TerminalCheckpoint struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// SupersedeCheckpoint 是 adapter 自描述、可持久化的旧展示位置收敛操作。
type SupersedeCheckpoint struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// StreamTerminalState 区分完成、失败和用户主动停止，避免 adapter 把停止渲染成错误。
type StreamTerminalState string

const (
	StreamTerminalCompleted StreamTerminalState = "completed"
	StreamTerminalFailed    StreamTerminalState = "failed"
	StreamTerminalStopped   StreamTerminalState = "stopped"
)

// DurableStreamReference 是可跨进程恢复同一张流式卡片的 adapter 自描述引用。
// 引用可以保存恢复终态所需的已展示卡片快照，但不得包含平台凭据或未脱敏协议正文。
type DurableStreamReference struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// DurableStreamReferenceExporter 在进程退出前导出仍处于进行态的卡片引用。
type DurableStreamReferenceExporter interface {
	DurableReference() (DurableStreamReference, error)
}

// DurableStreamReferenceChangeNotifier 允许 adapter 在审批等非进度路径改变可恢复卡片快照后，
// 通知消息层立即刷新持久化引用。handler 必须在 adapter 内部锁之外调用。
type DurableStreamReferenceChangeNotifier interface {
	SetDurableReferenceChangeHandler(handler func())
}

// DurableStreamSupersedePreparer 根据持久化引用生成可重放的旧展示位置收敛操作。
type DurableStreamSupersedePreparer interface {
	PrepareSupersedeFromReference(reference DurableStreamReference, notice string, operationID string) (SupersedeCheckpoint, error)
}

// PreparedSupersedableStream 关闭当前内存 stream，并投递已经持久化的收敛操作。
type PreparedSupersedableStream interface {
	DeliverPreparedSupersede(ctx context.Context, checkpoint SupersedeCheckpoint) error
}

// DurableSupersedeReplier 用重建后的平台客户端重放旧展示位置收敛操作。
type DurableSupersedeReplier interface {
	DeliverSupersede(ctx context.Context, checkpoint SupersedeCheckpoint) error
}

// DurableStreamTerminalPreparer 在新进程中根据持久化引用生成终态操作。
type DurableStreamTerminalPreparer interface {
	PrepareTerminalFromReference(reference DurableStreamReference, finalContent string, failed bool) (TerminalCheckpoint, error)
}

// StatefulDurableStreamTerminalPreparer 在跨进程恢复时保留完成、失败和停止三态。
type StatefulDurableStreamTerminalPreparer interface {
	DurableStreamTerminalPreparer
	PrepareTerminalFromReferenceWithState(reference DurableStreamReference, finalContent string, state StreamTerminalState) (TerminalCheckpoint, error)
}

// DurableTerminalStream 在执行网络写入前冻结并导出终态操作。
type DurableTerminalStream interface {
	PrepareTerminal(finalContent string, failed bool) (TerminalCheckpoint, error)
}

// StatefulDurableTerminalStream 为支持独立停止样式的 adapter 提供三态终态操作。
// DurableTerminalStream 保留为兼容降级；不支持三态的平台把 stopped 当作非失败完成。
type StatefulDurableTerminalStream interface {
	DurableTerminalStream
	PrepareTerminalWithState(finalContent string, state StreamTerminalState) (TerminalCheckpoint, error)
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

// StoppableStream 允许平台把用户主动停止渲染为独立终态，而不是 Complete 或 Fail。
type StoppableStream interface {
	Stop(ctx context.Context, finalContent string) error
}

// SupersedableStream 是流的可选能力，用于停止旧展示位置但不宣告任务终态。
type SupersedableStream interface {
	Supersede(ctx context.Context, notice string) error
}

// DetachableStream 允许平台在消息窗口解除绑定时冻结当前展示，但不把它标记为
// 自动续卡或位置迁移。未实现的平台可以继续使用 SupersedableStream 降级。
type DetachableStream interface {
	Detach(ctx context.Context, notice string) error
}

// DurableStreamDetachPreparer 根据持久化引用生成可重放的解除同步操作。
// 返回 SupersedeCheckpoint 是为了复用同一套非终态 outbox 重试通道。
type DurableStreamDetachPreparer interface {
	PrepareDetachFromReference(reference DurableStreamReference, notice string, operationID string) (SupersedeCheckpoint, error)
}

// StreamContentPreflighter 在网络更新前按平台的完整载荷限制校验正文。
// 不支持的平台无需实现；返回 ErrStreamContentTooLarge 时消息层可创建续接卡片。
type StreamContentPreflighter interface {
	PreflightUpdate(content string) error
}

type StreamPresentation struct {
	Summary string
	Preview string
	Details string
}

type StructuredProgressStream interface {
	UpdatePresentation(ctx context.Context, presentation StreamPresentation) error
}

type StreamPresentationPreflighter interface {
	PreflightPresentation(presentation StreamPresentation) error
}

// StreamOptions 描述流式回复的初始化参数。
type StreamOptions struct {
	Title               string
	InitialContent      string
	InitialPresentation *StreamPresentation
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
	// ChoiceMetadataTaskControlToken 将任务控制按钮绑定到服务端暂存消息快照。
	ChoiceMetadataTaskControlToken = "task_control_token"
	ChoiceInteractionTaskControl   = "task_control"
)

// Choice 表示一项可由用户选择的编号选项。
type Choice struct {
	ID       string
	Label    string
	Metadata map[string]string
}
