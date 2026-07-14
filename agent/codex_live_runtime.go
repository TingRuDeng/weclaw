package agent

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrCodexDesktopOwnershipUnknown = errors.New("Codex Desktop thread 所有权未知")
	ErrCodexTurnTerminal            = errors.New("Codex turn 已终止")
)

type CodexRuntimeOwner string

const (
	CodexOwnerUnknown             CodexRuntimeOwner = "unknown"
	CodexOwnerDesktopLive         CodexRuntimeOwner = "desktop_live"
	CodexOwnerDesktopDisconnected CodexRuntimeOwner = "desktop_disconnected"
	CodexOwnerWeClawRuntime       CodexRuntimeOwner = "weclaw_runtime"
	CodexOwnerPersistedOnly       CodexRuntimeOwner = "persisted_only"
)

type CodexThreadRef struct {
	ConversationID string `json:"conversationId"`
	ThreadID       string `json:"threadId"`
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
	Ref              CodexThreadRef    `json:"ref"`
	Owner            CodexRuntimeOwner `json:"owner"`
	OwnerRevision    uint64            `json:"ownerRevision"`
	Connected        bool              `json:"connected"`
	ReleaseConfirmed bool              `json:"releaseConfirmed"`
	State            CodexThreadState  `json:"state"`
}

type CodexLiveRuntimeAgent interface {
	BindCodexThread(context.Context, CodexThreadRef) (CodexThreadBinding, error)
	CurrentCodexThreadBinding(string) (CodexThreadBinding, bool)
	RecoverCodexThread(context.Context, CodexThreadRef) error
}

type codexDesktopOwnerProbe interface {
	Discover(context.Context, CodexThreadRef) (bool, error)
	LoadHistory(context.Context, CodexThreadRef) error
	Presence() (socketExists bool, processExists bool)
}

// BindCodexThread 只读探测并保存 conversation 到 thread 的 owner binding。
func (a *ACPAgent) BindCodexThread(ctx context.Context, ref CodexThreadRef) (CodexThreadBinding, error) {
	if a.protocol != protocolCodexAppServer || a.codexOwners == nil {
		return CodexThreadBinding{}, ErrCodexDesktopOwnershipUnknown
	}
	binding, err := a.codexOwners.bind(ctx, ref)
	a.persistState()
	return binding, err
}

// CurrentCodexThreadBinding 返回当前 conversation 的 live runtime binding。
func (a *ACPAgent) CurrentCodexThreadBinding(conversationID string) (CodexThreadBinding, bool) {
	if a.codexOwners == nil {
		return CodexThreadBinding{}, false
	}
	return a.codexOwners.currentConversationBinding(conversationID)
}
