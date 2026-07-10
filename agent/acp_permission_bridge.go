package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

func (a *ACPAgent) handlePermissionRequest(raw string) {
	var req struct {
		ID     json.RawMessage         `json:"id"`
		Method string                  `json:"method"`
		Params permissionRequestParams `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		log.Printf("[acp] failed to parse permission request: %v", err)
		return
	}

	responseFormat := permissionResponseFormatForMethod(req.Method)
	options := approvalOptionsFromPermission(req.Params)
	if responseFormat == permissionResponsePermissions {
		options = codexPermissionApprovalOptions()
	}
	approval := &codexApprovalRequest{
		ID:                   req.ID,
		ResponseFormat:       responseFormat,
		RequestedPermissions: req.Params.Permissions,
		Request: ApprovalRequest{
			ToolCall: permissionToolCall(req.Params),
			Options:  options,
		},
	}
	if a.dispatchToTurnCh(permissionRouteID(req.Params), &codexTurnEvent{Kind: "approval_request", Approval: approval}) {
		return
	}
	optionID := selectPermissionOption(req.Params, defaultDenyDecision(approval.Request.Options))
	if err := a.respondPermissionRequest(req.ID, optionID, responseFormat, req.Params.Permissions); err != nil {
		log.Printf("[acp] failed to deny unroutable permission request: %v", err)
	}
}

// UnmarshalJSON 兼容 Codex command 审批字段的新旧形态：字符串数组或单个命令字符串。
func (c *permissionCommand) UnmarshalJSON(data []byte) error {
	var parts []string
	if err := json.Unmarshal(data, &parts); err == nil {
		*c = permissionCommand(parts)
		return nil
	}
	var command string
	if err := json.Unmarshal(data, &command); err == nil {
		command = strings.TrimSpace(command)
		if command == "" {
			*c = nil
			return nil
		}
		*c = permissionCommand{command}
		return nil
	}
	var object struct {
		Cmd     string   `json:"cmd"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*c = permissionCommandFromObject(object.Cmd, object.Command, object.Args)
	return nil
}

func permissionCommandFromObject(cmd string, command string, args []string) permissionCommand {
	if value := firstNonEmptyString(cmd, command); value != "" {
		return permissionCommand{value}
	}
	result := make(permissionCommand, 0, len(args))
	for _, arg := range args {
		if trimmed := strings.TrimSpace(arg); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// UnmarshalJSON 兼容 Codex availableDecisions 字段：字符串数组或带 decision 的对象数组。
func (d *permissionDecisions) UnmarshalJSON(data []byte) error {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &rawItems); err == nil {
		*d = parsePermissionDecisionItems(rawItems)
		return nil
	}
	decision, ok, err := parsePermissionDecisionValue(data)
	if err != nil {
		return err
	}
	if !ok {
		*d = nil
		return nil
	}
	*d = permissionDecisions{decision}
	return nil
}

// parsePermissionDecisionItems 逐项提取新版审批 decision，跳过空对象。
func parsePermissionDecisionItems(items []json.RawMessage) permissionDecisions {
	decisions := make(permissionDecisions, 0, len(items))
	for _, item := range items {
		if decision, ok, _ := parsePermissionDecisionValue(item); ok {
			decisions = append(decisions, decision)
		}
	}
	return decisions
}

// parsePermissionDecisionValue 从字符串或对象中取出实际要回传给 Codex 的 decision。
func parsePermissionDecisionValue(data json.RawMessage) (string, bool, error) {
	var decision string
	if err := json.Unmarshal(data, &decision); err == nil {
		return strings.TrimSpace(decision), strings.TrimSpace(decision) != "", nil
	}
	var object struct {
		Decision string `json:"decision"`
		ID       string `json:"id"`
		OptionID string `json:"optionId"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return "", false, err
	}
	decision = firstNonEmptyString(object.Decision, object.ID, object.OptionID, object.Value)
	return decision, decision != "", nil
}

// firstNonEmptyString 返回第一个非空字符串，用于兼容不同对象字段名。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (a *ACPAgent) resolvePermissionOption(ctx context.Context, req ApprovalRequest) string {
	fallback := selectApprovalOption(req.Options, defaultDenyDecision(req.Options))
	if len(req.Options) == 0 {
		return fallback
	}
	handler := approvalHandlerFromContext(ctx)
	if handler == nil {
		return fallback
	}
	optionID, err := handler(ctx, req)
	if err != nil {
		log.Printf("[acp] approval handler failed, denying request: %v", err)
		return fallback
	}
	if isApprovalOption(req.Options, optionID) {
		return optionID
	}
	log.Printf("[acp] approval handler returned unknown option %q, denying request", optionID)
	return fallback
}

func (a *ACPAgent) respondPermissionRequest(id json.RawMessage, optionID string, responseFormat permissionResponseFormat, requested ...json.RawMessage) error {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if responseFormat == permissionResponsePermissions {
		permissions, err := grantedCodexPermissions(optionID, requested)
		if err != nil {
			return err
		}
		resp["result"] = map[string]interface{}{"permissions": permissions}
	} else if responseFormat == permissionResponseDecision {
		resp["result"] = map[string]interface{}{
			"decision": optionID,
		}
	} else {
		outcome := map[string]interface{}{"outcome": "cancelled"}
		if strings.TrimSpace(optionID) != "" && !strings.EqualFold(optionID, "decline") {
			outcome = map[string]interface{}{"outcome": "selected", "optionId": optionID}
		}
		resp["result"] = map[string]interface{}{"outcome": outcome}
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal permission response: %w", err)
	}
	return a.writeJSONLine(data)
}

func grantedCodexPermissions(optionID string, requested []json.RawMessage) (interface{}, error) {
	if approvalKindFromDecision(optionID) != "allow" || len(requested) == 0 || len(requested[0]) == 0 {
		return map[string]interface{}{}, nil
	}
	var permissions interface{}
	if err := json.Unmarshal(requested[0], &permissions); err != nil {
		return nil, fmt.Errorf("parse requested permissions: %w", err)
	}
	return permissions, nil
}

// permissionRouteID 按协议类型选择会话键，标准 ACP 使用 sessionId，Codex 使用 threadId。
func permissionRouteID(params permissionRequestParams) string {
	return firstNonEmptyString(params.ThreadID, params.SessionID)
}

// permissionResponseFormatForMethod 区分旧 ACP 和新版 Codex item 审批响应结构。
func permissionResponseFormatForMethod(method string) permissionResponseFormat {
	switch method {
	case "item/permissions/requestApproval":
		return permissionResponsePermissions
	case "item/fileChange/requestApproval", "item/commandExecution/requestApproval":
		return permissionResponseDecision
	default:
		return permissionResponseOutcome
	}
}

// permissionToolCall 为新版审批请求补出可读命令，避免飞书按钮只显示泛化提示。
func permissionToolCall(params permissionRequestParams) json.RawMessage {
	if len(params.ToolCall) > 0 && string(params.ToolCall) != "null" {
		return params.ToolCall
	}
	if len(params.Permissions) > 0 && string(params.Permissions) != "null" {
		tool := map[string]interface{}{
			"cwd":         strings.TrimSpace(params.Cwd),
			"reason":      strings.TrimSpace(params.Reason),
			"permissions": params.Permissions,
		}
		data, err := json.Marshal(tool)
		if err == nil {
			return data
		}
	}
	tool := map[string]interface{}{}
	if len(params.Command) > 0 {
		tool["cmd"] = strings.Join(params.Command, " ")
	}
	if strings.TrimSpace(params.Cwd) != "" {
		tool["cwd"] = params.Cwd
	}
	if len(tool) == 0 {
		return nil
	}
	data, err := json.Marshal(tool)
	if err != nil {
		return nil
	}
	return data
}

// approvalOptionsFromPermission 统一旧 options 和新版 availableDecisions。
func approvalOptionsFromPermission(params permissionRequestParams) []ApprovalOption {
	decisions := permissionAvailableDecisions(params)
	result := make([]ApprovalOption, 0, len(params.Options)+len(decisions))
	for _, opt := range params.Options {
		result = append(result, ApprovalOption{ID: opt.OptionID, Name: opt.Name, Kind: approvalKindFromDecision(opt.Kind)})
	}
	for _, decision := range decisions {
		decision = strings.TrimSpace(decision)
		if decision == "" {
			continue
		}
		result = append(result, ApprovalOption{ID: decision, Name: decision, Kind: approvalKindFromDecision(decision)})
	}
	return result
}

// permissionAvailableDecisions 兼容 Codex app-server 新旧字段名，避免新版 snake_case 审批被误判为无选项。
func permissionAvailableDecisions(params permissionRequestParams) permissionDecisions {
	if len(params.AvailableDecisions) > 0 {
		return params.AvailableDecisions
	}
	return params.AvailableDecisionsSnake
}

// approvalKindFromDecision 把新版 decision 字符串映射到通用允许/拒绝类型。
func approvalKindFromDecision(decision string) string {
	lower := strings.ToLower(strings.TrimSpace(decision))
	switch {
	case strings.Contains(lower, "cancel"), strings.Contains(lower, "decline"), strings.Contains(lower, "deny"), strings.Contains(lower, "reject"):
		return "deny"
	case strings.Contains(lower, "accept"), strings.Contains(lower, "allow"), strings.Contains(lower, "approve"):
		return "allow"
	default:
		return lower
	}
}
