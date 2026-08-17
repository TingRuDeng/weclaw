package agent

import (
	"context"
	"encoding/json"
	"errors"
)

type approvalContextKey struct{}

// ApprovalOption 描述 Codex 提供的一项审批选择。
type ApprovalOption struct {
	ID   string
	Name string
	Kind string
}

type ApprovalRequestState uint8

const (
	ApprovalRequestStateUnknown ApprovalRequestState = iota
	ApprovalRequestStatePending
	ApprovalRequestStateResolvedExternally
	ApprovalRequestStateTurnTerminal
)

var (
	ErrApprovalResolvedExternally = errors.New("审批已由其他前端处理")
	ErrApprovalTurnTerminal       = errors.New("审批所属 Codex turn 已结束")
)

type ApprovalRequestStateProbe func(context.Context) (ApprovalRequestState, error)

// ApprovalRequest 描述一次需要用户确认的 Codex 敏感操作。
type ApprovalRequest struct {
	RequestID  string
	ToolCall   json.RawMessage
	Options    []ApprovalOption
	StateProbe ApprovalRequestStateProbe
	Resolution CodexInteractionResolution
}

// ApprovalHandler 由消息层实现，用于把 Codex 审批请求转成平台交互。
type ApprovalHandler func(context.Context, ApprovalRequest) (string, error)

// ContextWithApprovalHandler 为当前 turn 注入审批处理器。
func ContextWithApprovalHandler(ctx context.Context, handler ApprovalHandler) context.Context {
	if handler == nil {
		return ctx
	}
	return context.WithValue(ctx, approvalContextKey{}, handler)
}

func approvalHandlerFromContext(ctx context.Context) ApprovalHandler {
	handler, _ := ctx.Value(approvalContextKey{}).(ApprovalHandler)
	return handler
}

func approvalPolicyForContext(ctx context.Context) string {
	if approvalHandlerFromContext(ctx) != nil {
		return "untrusted"
	}
	return "on-request"
}
