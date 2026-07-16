package messaging

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
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
	toolCall := strings.TrimSpace(string(req.ToolCall))
	if toolCall == "" {
		toolCall = displayName + " 请求执行一项需要确认的操作。"
	} else if len([]rune(toolCall)) > 1200 {
		runes := []rune(toolCall)
		toolCall = string(runes[:1200]) + "..."
	}
	return displayName + " 请求执行敏感操作，请确认：\n\n" + toolCall
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
