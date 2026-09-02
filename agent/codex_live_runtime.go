package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrCodexDesktopOwnershipUnknown      = errors.New("Codex Desktop thread 所有权未知")
	ErrCodexDesktopCapabilityUnavailable = errors.New("Codex App IPC 不支持该操作")
	ErrCodexTurnTerminal                 = errors.New("Codex turn 已终止")
	ErrCodexControlChanged               = errors.New("Codex 控制权已变化")
	ErrCodexControlRequired              = errors.New("当前窗口没有 Codex 远程控制权")
	ErrCodexRuntimeConflict              = errors.New("Codex Desktop 与 WeClaw 发生写入冲突")
	ErrCodexRuntimeUnavailable           = errors.New("Codex 实际运行时不可用")
	ErrCodexNoActiveTurn                 = errors.New("Codex thread 当前没有活动 turn")
	ErrCodexWriterBusy                   = errors.New("Codex thread 已有写入任务")
	ErrCodexDesktopAdoptionDeferred      = errors.New("Codex App Host 接入等待共享 Host 空闲")
	ErrCodexInputDeliveryUnknown         = errors.New("Codex 输入交付状态未知")
	ErrCodexInputDeliveryUnconfirmed     = errors.New("Codex 输入是否已接收无法确认")
)

type CodexControlOwner string

const (
	CodexControlUnclaimed CodexControlOwner = "unclaimed"
	CodexControlDesktop   CodexControlOwner = "desktop"
	CodexControlRemote    CodexControlOwner = "remote"
)

type CodexRuntimeHolder string

const (
	CodexRuntimeUnknown  CodexRuntimeHolder = "unknown"
	CodexRuntimeDesktop  CodexRuntimeHolder = "desktop"
	CodexRuntimeWeClaw   CodexRuntimeHolder = "weclaw"
	CodexRuntimeConflict CodexRuntimeHolder = "conflict"
)

type CodexThreadRef struct {
	ConversationID string `json:"conversationId"`
	ThreadID       string `json:"threadId"`
}

type CodexControlIntent struct {
	Owner          CodexControlOwner `json:"owner"`
	RouteKey       string            `json:"routeKey,omitempty"`
	ConversationID string            `json:"conversationId,omitempty"`
	Revision       uint64            `json:"revision"`
}

type CodexRolloutCheckpoint struct {
	Path   string `json:"path,omitempty"`
	TurnID string `json:"turnId,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Active bool   `json:"active,omitempty"`
}

type CodexRuntimeRequest struct {
	Ref        CodexThreadRef
	Intent     CodexControlIntent
	Checkpoint CodexRolloutCheckpoint
	// WorkspaceRoot determines the effective current provider through config/read.
	// Empty preserves compatibility for callers that do not manage local Codex state.
	WorkspaceRoot string
	// PendingFirstTurn 表示该 thread 尚无已接受的用户 turn，可在协议确认不存在时安全补建。
	PendingFirstTurn bool
}

type CodexProviderPreparation struct {
	Provider         string
	PreviousProvider string
	Changed          bool
	Deferred         bool
	TargetActive     bool
	BackupDir        string
}

type CodexTurnRequest struct {
	Runtime    CodexRuntimeRequest
	Message    string
	OnProgress func(string)
	// OnProgressEvent 优先于 OnProgress；保留旧字段供现有调用方兼容。
	OnProgressEvent func(ProgressEvent)
	// OnThreadReplaced 在空 thread 补建后、首个 turn 启动前原子迁移外层持久化选择。
	OnThreadReplaced func(previous CodexThreadRef, current CodexThreadRef) error
	// OnTurnStarted 在协议返回真实 turn ID 后同步外层首次写入生命周期。
	OnTurnStarted func(thread CodexThreadRef, turnID string) error
	// OnTurnSteered 在同一输入因多前端竞态加入现有 active turn 后同步外层生命周期。
	OnTurnSteered func(thread CodexThreadRef, turnID string) error
	AttemptID     string
	MessageKey    string
	// OnInputAttempt persists only delivery metadata; Message is deliberately
	// absent so a restart can never auto-execute saved user input.
	OnInputAttempt func(CodexInputAttempt) error
}

type CodexInputAttemptStatus string

const (
	CodexInputAttemptPending     CodexInputAttemptStatus = "pending"
	CodexInputAttemptAccepted    CodexInputAttemptStatus = "accepted"
	CodexInputAttemptUnconfirmed CodexInputAttemptStatus = "unconfirmed"
	CodexInputAttemptRejected    CodexInputAttemptStatus = "rejected"
)

type CodexInputAttempt struct {
	AttemptID      string
	MessageKey     string
	ThreadID       string
	BaselineTurnID string
	ExpectedTurnID string
	MessageDigest  string
	Status         CodexInputAttemptStatus
}

// CodexTurnInterruptedError 表示 app-server 的观察流中断，最终结果仍需由调用方核对。
type CodexTurnInterruptedError struct {
	ThreadID string
	TurnID   string

	confirmOnce sync.Once
	onConfirmed func()
}

func (e *CodexTurnInterruptedError) Error() string {
	return fmt.Sprintf("Codex turn 已中断（thread=%s, turn=%s）", e.ThreadID, e.TurnID)
}

// ConfirmTerminal releases the fail-closed writer lease only after a watcher
// has authoritative evidence that the interrupted turn reached a terminal
// state. Errors created outside RunCodexTurn intentionally make this a no-op.
func (e *CodexTurnInterruptedError) ConfirmTerminal() {
	if e == nil {
		return
	}
	e.confirmOnce.Do(func() {
		if e.onConfirmed != nil {
			e.onConfirmed()
		}
	})
}

func (e *CodexTurnInterruptedError) setTerminalConfirmation(confirm func()) {
	if e != nil {
		e.onConfirmed = confirm
	}
}

type CodexThreadBinding struct {
	Ref               CodexThreadRef     `json:"ref"`
	State             CodexThreadState   `json:"state"`
	Control           CodexControlIntent `json:"-"`
	Runtime           CodexRuntimeHolder `json:"-"`
	RuntimeGeneration uint64             `json:"-"`
	ConflictReason    string             `json:"-"`
}

type CodexLiveRuntimeAgent interface {
	InspectCodexRuntime(context.Context, CodexRuntimeRequest) (CodexThreadBinding, error)
	CurrentCodexRuntime(CodexRuntimeRequest) (CodexThreadBinding, error)
	HandoffCodexRuntime(context.Context, CodexRuntimeRequest) (CodexThreadBinding, error)
	ReconcileCodexObservedTurn(context.Context, CodexRuntimeRequest, CodexThreadState) (CodexThreadBinding, error)
	MarkCodexRuntimeConflict(context.Context, CodexRuntimeRequest) error
	RunCodexTurn(context.Context, CodexTurnRequest) (string, error)
}

// CodexInputSteeringAgent submits one input to the authoritative active turn
// without creating another observer. It is used when an existing task already
// owns the frontend progress lifecycle.
type CodexInputSteeringAgent interface {
	SteerCodexInput(context.Context, CodexTurnRequest) (turnID string, err error)
}

// CodexProviderRuntimeAgent is implemented by local Codex app-server agents
// that can migrate one persisted thread to the Host's effective provider.
type CodexProviderRuntimeAgent interface {
	PrepareCodexThread(context.Context, CodexRuntimeRequest) (CodexProviderPreparation, error)
}

// CodexThreadSubscriptionAgent removes only the current app-server client's
// subscription. It does not transfer writer ownership or restart the Host.
type CodexThreadSubscriptionAgent interface {
	UnsubscribeCodexThread(context.Context, string) (attempted bool, err error)
}

// CodexThreadObserverSubscriptionAgent establishes the current app-server
// client's observer subscription after a frontend binding has committed.
type CodexThreadObserverSubscriptionAgent interface {
	SubscribeCodexThread(context.Context, string, string) (attempted bool, err error)
}

type codexDesktopOwnerProbe interface {
	LoadHistory(context.Context, CodexThreadRef) error
	LoadHistoryForActiveWriter(context.Context, CodexThreadRef) error
	Presence() (socketExists bool, processExists bool)
}
