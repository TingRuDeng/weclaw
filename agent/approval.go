package agent

import (
	"context"
	"encoding/json"
)

type approvalContextKey struct{}

// ApprovalOption 描述 Codex 提供的一项审批选择。
type ApprovalOption struct {
	ID   string
	Name string
	Kind string
}

// ApprovalRequest 描述一次需要用户确认的 Codex 敏感操作。
type ApprovalRequest struct {
	ToolCall json.RawMessage
	Options  []ApprovalOption
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
	return "never"
}
