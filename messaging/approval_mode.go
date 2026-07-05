package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// handleModeCommand 查看或切换当前用户的确认模式（yolo 自动放行 / default 按钮确认）。
func (h *Handler) handleModeCommand(userID string, trimmed string) string {
	fields := strings.Fields(trimmed)
	if len(fields) == 1 {
		if h.isYoloMode(userID) {
			return "当前确认模式：yolo（自动放行 Codex 敏感操作）。\n发送 /mode default 恢复按钮确认。"
		}
		return "当前确认模式：default（每次敏感操作弹按钮确认）。\n发送 /mode yolo 自动放行。"
	}
	switch strings.ToLower(strings.TrimSpace(fields[1])) {
	case "yolo":
		h.setYoloMode(userID, true)
		h.auditRecord(auditEntry{User: userID, Action: "mode_yolo_enabled"})
		return "已切换为 yolo 模式：Codex 敏感操作将自动放行。\n⚠️ 该模式跳过确认，请确保当前会话可信。发送 /mode default 可恢复确认。"
	case "default", "ask", "off":
		h.setYoloMode(userID, false)
		return "已切换为 default 模式：Codex 敏感操作会弹按钮确认。"
	default:
		return "用法：/mode 查看当前确认模式；/mode yolo 自动放行；/mode default 按钮确认。"
	}
}

func (h *Handler) setYoloMode(userID string, on bool) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	if on {
		h.yoloUsers.Store(userID, struct{}{})
		return
	}
	h.yoloUsers.Delete(userID)
}

func (h *Handler) isYoloMode(userID string) bool {
	_, ok := h.yoloUsers.Load(strings.TrimSpace(userID))
	return ok
}

// autoApproveApprovalOption 在 yolo 模式下选择允许类选项；没有显式 allow 时退回首个选项。
func autoApproveApprovalOption(options []agent.ApprovalOption) string {
	for _, option := range options {
		if option.Kind == "allow" && strings.TrimSpace(option.ID) != "" {
			return option.ID
		}
	}
	if len(options) > 0 {
		return options[0].ID
	}
	return ""
}
