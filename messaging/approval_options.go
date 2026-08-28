package messaging

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

func staleApprovalReply() string {
	return "这次交互已过期或原任务已结束，没有再发送给 Agent。\n\n请重新发起任务。"
}

func approvalOptionSet(options []agent.ApprovalOption) map[string]bool {
	allowed := make(map[string]bool, len(options))
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id != "" {
			allowed[id] = true
		}
	}
	return allowed
}

func approvalOptionAliases(options []agent.ApprovalOption) map[string]string {
	aliases := make(map[string]string)
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		aliases[strings.ToLower(id)] = id
		switch approvalOptionKind(option) {
		case "allow":
			for _, alias := range []string{"accept", "accepted", "approve", "approved", "allow"} {
				if aliases[alias] == "" {
					aliases[alias] = id
				}
			}
		case "deny":
			for _, alias := range []string{"cancel", "cancelled", "deny", "denied", "reject", "rejected"} {
				if aliases[alias] == "" {
					aliases[alias] = id
				}
			}
		}
	}
	return aliases
}

func approvalOptionKind(option agent.ApprovalOption) string {
	lower := strings.ToLower(strings.TrimSpace(firstNonBlank(option.Kind, option.ID, option.Name)))
	switch {
	case strings.Contains(lower, "accept"), strings.Contains(lower, "allow"), strings.Contains(lower, "approve"):
		return "allow"
	case strings.Contains(lower, "cancel"), strings.Contains(lower, "deny"), strings.Contains(lower, "reject"):
		return "deny"
	default:
		return lower
	}
}

func approvalPrompt(req agent.ApprovalRequest, agentName string) string {
	displayName := agentDisplayName(agentName)
	if prompt := structuredApprovalPrompt(req, displayName); prompt != "" {
		return prompt
	}
	toolCall := strings.TrimSpace(string(req.ToolCall))
	if toolCall == "" {
		toolCall = displayName + " 请求执行一项需要确认的操作。"
	} else if len([]rune(toolCall)) > 1200 {
		runes := []rune(toolCall)
		toolCall = string(runes[:1200]) + "..."
	}
	return displayName + " 请求执行敏感操作，请确认：\n\n" + toolCall
}

func structuredApprovalPrompt(req agent.ApprovalRequest, displayName string) string {
	ctx := req.Context
	if strings.TrimSpace(ctx.Reason) == "" && strings.TrimSpace(ctx.Operation) == "" && len(ctx.Command) == 0 && strings.TrimSpace(ctx.Cwd) == "" && len(ctx.Permissions) == 0 {
		return ""
	}
	lines := []string{displayName + " 请求执行敏感操作，请确认：", ""}
	if reason := approvalPurpose(req); reason != "" {
		lines = append(lines, "申请目的："+reason)
	}
	if operation := approvalDisplayText(ctx.Operation); operation != "" {
		lines = append(lines, "操作类型："+operation)
	}
	if command := approvalDisplayText(strings.Join(ctx.Command, " ")); command != "" {
		lines = append(lines, "命令："+command)
	}
	if cwd := approvalDisplayText(ctx.Cwd); cwd != "" {
		lines = append(lines, "工作目录："+cwd)
	}
	if len(ctx.Permissions) > 0 && strings.TrimSpace(string(ctx.Permissions)) != "null" {
		lines = append(lines, "权限范围：申请额外运行权限")
	}
	if detail := approvalToolCallDetail(req.ToolCall, ctx); detail != "" {
		lines = append(lines, "操作详情："+detail)
	}
	impact := approvalImpact(ctx.Operation)
	if impact != "" {
		lines = append(lines, "可能影响："+impact)
	}
	lines = append(lines, "", "请确认是否继续。")
	return strings.Join(lines, "\n")
}

func approvalToolCallDetail(raw json.RawMessage, ctx agent.ApprovalContext) string {
	if len(ctx.Command) > 0 || strings.TrimSpace(ctx.Cwd) != "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return approvalDisplayText(string(raw))
	}
	for _, key := range []string{"cmd", "command", "path", "file"} {
		if value, ok := payload[key].(string); ok {
			if value = approvalDisplayText(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func approvalPurpose(req agent.ApprovalRequest) string {
	if reason := approvalDisplayText(req.Context.Reason); reason != "" {
		return reason
	}
	var payload map[string]any
	if err := json.Unmarshal(req.ToolCall, &payload); err == nil {
		for _, key := range []string{"title", "description"} {
			if value, ok := payload[key].(string); ok {
				if value = approvalDisplayText(value); value != "" {
					return value
				}
			}
		}
	}
	return "上游未提供具体原因，请根据操作内容判断。"
}

func approvalImpact(operation string) string {
	switch strings.TrimSpace(operation) {
	case "命令执行":
		return "将在该目录执行命令，可能读取项目文件并生成临时文件。"
	case "文件修改":
		return "将修改工作区中的文件内容。"
	case "文件读取":
		return "将读取工作区中的文件内容。"
	case "权限申请":
		return "将扩大本次操作可使用的运行权限。"
	default:
		return "可能改变当前任务的工作区或运行环境。"
	}
}

func approvalDisplayText(value string) string {
	value = observability.SanitizeText(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	if runes := []rune(value); len(runes) > 500 {
		return string(runes[:497]) + "..."
	}
	return value
}

func approvalChoices(options []agent.ApprovalOption, approvalKey string, taskCardID string, ownerUserID string, routeUserID string, agentName string) []platform.Choice {
	choices := make([]platform.Choice, 0, len(options))
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		choice := platform.Choice{ID: id, Label: approvalChoiceLabel(option)}
		metadata := approvalChoiceMetadata(
			approvalKey, taskCardID, ownerUserID, routeUserID,
			agentName, platform.ChoiceInteractionApproval,
		)
		if len(metadata) > 0 {
			choice.Metadata = metadata
		}
		choices = append(choices, choice)
	}
	return choices
}

func approvalChoiceForDecision(choices []platform.Choice, decision string) (platform.Choice, bool) {
	decision = strings.TrimSpace(decision)
	for _, choice := range choices {
		if strings.TrimSpace(choice.ID) == decision && decision != "" {
			return choice, true
		}
	}
	return platform.Choice{}, false
}

func approvalChoiceMetadata(approvalKey string, taskCardID string, ownerUserID string, routeUserID string, agentName string, interactionKind string) map[string]string {
	metadata := make(map[string]string, 6)
	if approvalKey = strings.TrimSpace(approvalKey); approvalKey != "" {
		metadata["approval_key"] = approvalKey
	}
	if taskCardID = strings.TrimSpace(taskCardID); taskCardID != "" {
		metadata["task_card_id"] = taskCardID
	}
	if ownerUserID = strings.TrimSpace(ownerUserID); ownerUserID != "" {
		metadata["approval_owner"] = ownerUserID
	}
	if sessionKey := feishuSessionKeyFromRoute(routeUserID); sessionKey != "" {
		metadata[feishuSessionMetadataKey] = sessionKey
	}
	if agentName = strings.TrimSpace(agentName); agentName != "" {
		metadata[platform.ChoiceMetadataAgentName] = agentDisplayName(agentName)
	}
	if interactionKind = strings.TrimSpace(interactionKind); interactionKind != "" {
		metadata[platform.ChoiceMetadataInteractionKind] = interactionKind
	}
	return metadata
}

func taskCardIDFromReplier(reply platform.Replier) string {
	reporter, ok := reply.(platform.TaskCardReporter)
	if !ok {
		return ""
	}
	return strings.TrimSpace(reporter.CurrentTaskCardID())
}

func approvalPendingKey(requestID string) string {
	// 请求 ID 便于关联上游审批，随机 nonce 保证不同 Agent 的同号请求也不会碰撞。
	sum := sha256.Sum256([]byte(strings.TrimSpace(requestID) + "\x00" + uuid.NewString()))
	return hex.EncodeToString(sum[:])
}

func pendingApprovalMapKey(userID string, routeUserID string, interactionKind string, approvalKey string) string {
	userID = strings.TrimSpace(userID)
	routeUserID = strings.TrimSpace(routeUserID)
	interactionKind = strings.TrimSpace(interactionKind)
	approvalKey = strings.TrimSpace(approvalKey)
	if userID == "" || approvalKey == "" {
		return ""
	}
	return strings.Join([]string{userID, routeUserID, interactionKind, approvalKey}, "\x00")
}

// approvalChoiceLabel 根据上游选项 ID 保留授权范围，避免不同允许语义显示为同一按钮。
func approvalChoiceLabel(option agent.ApprovalOption) string {
	switch approvalOptionKind(option) {
	case "allow":
		return approvalAllowChoiceLabel(option)
	case "deny":
		return "拒绝"
	default:
		return firstNonBlank(option.Name, option.Kind, option.ID)
	}
}

// approvalAllowChoiceLabel 区分 Claude 的持久授权与单次授权，并保留持久授权的具体范围。
func approvalAllowChoiceLabel(option agent.ApprovalOption) string {
	id := strings.ToLower(strings.TrimSpace(option.ID))
	name := strings.TrimSpace(option.Name)
	if id == "allow_always" || strings.HasPrefix(strings.ToLower(name), "always allow") {
		scope := ""
		if strings.HasPrefix(strings.ToLower(name), "always allow") {
			scope = strings.TrimSpace(name[len("Always Allow"):])
		}
		if scope == "" {
			return "始终允许"
		}
		return "始终允许：" + scope
	}
	if id == "allow" || id == "allow_once" || id == "allow-once" {
		return "仅本次允许"
	}
	return firstNonBlank(name, "允许")
}

func defaultDenyApprovalOption(options []agent.ApprovalOption) string {
	for _, option := range options {
		if approvalOptionKind(option) == "deny" && strings.TrimSpace(option.ID) != "" {
			return strings.TrimSpace(option.ID)
		}
	}
	// 没有明确拒绝项时使用协议级拒绝值，禁止回退到可能代表允许的首项。
	return "decline"
}
