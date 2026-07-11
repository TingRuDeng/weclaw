package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

var codexDesktopApprovalMethods = map[string]string{
	"item/commandExecution/requestApproval": "thread-follower-command-approval-decision",
	"item/fileChange/requestApproval":       "thread-follower-file-approval-decision",
	"item/fileRead/requestApproval":         "thread-follower-file-approval-decision",
	"item/permissions/requestApproval":      "thread-follower-file-approval-decision",
}

var codexDesktopApprovalDecisions = map[string]bool{
	"accept": true, "acceptForSession": true, "decline": true, "cancel": true,
}

type codexDesktopApprovalPayload struct {
	ConversationID string `json:"conversationId"`
	RequestID      string `json:"requestId"`
	Decision       string `json:"decision"`
}

// approvalEvent 把 Desktop pending action 投影为统一审批事件和 follower responder。
func (a *codexDesktopActions) approvalEvent(threadID string, action codexDesktopPendingAction) (*codexTurnEvent, error) {
	replyMethod, ok := codexDesktopApprovalMethods[action.Method]
	if !ok {
		return nil, fmt.Errorf("Desktop action %q 不是审批请求", action.Method)
	}
	params, err := decodeCodexDesktopPermissionParams(action.Params)
	if err != nil {
		return nil, err
	}
	approval := &codexApprovalRequest{
		Request: ApprovalRequest{
			RequestID: action.ID, ToolCall: permissionToolCall(params),
			Options: approvalOptionsFromPermission(params),
		},
	}
	approval.Respond = func(ctx context.Context, decision string) error {
		return a.respondApproval(ctx, replyMethod, codexDesktopApprovalPayload{
			ConversationID: strings.TrimSpace(threadID), RequestID: action.ID, Decision: decision,
		})
	}
	return &codexTurnEvent{Kind: "approval_request", Approval: approval}, nil
}

// respondApproval 校验 Desktop 支持的 decision 后只发送一次响应。
func (a *codexDesktopActions) respondApproval(ctx context.Context, method string, payload codexDesktopApprovalPayload) error {
	if !codexDesktopApprovalDecisions[payload.Decision] {
		return fmt.Errorf("Codex Desktop 审批 decision %q 无效", payload.Decision)
	}
	_, err := a.client.Call(ctx, method, payload)
	return err
}

// decodeCodexDesktopPermissionParams 复用 app-server 审批字段兼容逻辑。
func decodeCodexDesktopPermissionParams(raw map[string]any) (permissionRequestParams, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return permissionRequestParams{}, fmt.Errorf("编码 Desktop 审批参数: %w", err)
	}
	var params permissionRequestParams
	if err := json.Unmarshal(encoded, &params); err != nil {
		return params, fmt.Errorf("解析 Desktop 审批参数: %w", err)
	}
	return params, nil
}
