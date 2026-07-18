package agent

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrCodexDesktopOwnershipUnknown = errors.New("Codex Desktop thread 所有权未知")
	ErrCodexTurnTerminal            = errors.New("Codex turn 已终止")
	ErrCodexControlChanged          = errors.New("Codex 控制权已变化")
	ErrCodexControlRequired         = errors.New("当前窗口没有 Codex 远程控制权")
	ErrCodexRuntimeConflict         = errors.New("Codex Desktop 与 WeClaw 发生写入冲突")
	ErrCodexRuntimeUnavailable      = errors.New("Codex 实际运行时不可用")
	ErrCodexWriterBusy              = errors.New("Codex thread 已有写入任务")
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
	// PendingFirstTurn 表示该 thread 尚无已接受的用户 turn，可在协议确认不存在时安全补建。
	PendingFirstTurn bool
}

type CodexTurnRequest struct {
	Runtime    CodexRuntimeRequest
	Message    string
	OnProgress func(string)
	// OnThreadReplaced 在空 thread 补建后、首个 turn 启动前原子迁移外层持久化选择。
	OnThreadReplaced func(previous CodexThreadRef, current CodexThreadRef) error
	// OnTurnStarted 在协议返回真实 turn ID 后同步外层首次写入生命周期。
	OnTurnStarted func(thread CodexThreadRef, turnID string) error
}

// CodexTurnInterruptedError 表示 app-server 的观察流中断，最终结果仍需由调用方核对。
type CodexTurnInterruptedError struct {
	ThreadID string
	TurnID   string
}

func (e *CodexTurnInterruptedError) Error() string {
	return fmt.Sprintf("Codex turn 已中断（thread=%s, turn=%s）", e.ThreadID, e.TurnID)
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

type codexDesktopOwnerProbe interface {
	LoadHistory(context.Context, CodexThreadRef) error
	Presence() (socketExists bool, processExists bool)
}
