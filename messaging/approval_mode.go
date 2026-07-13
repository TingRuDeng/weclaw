package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// handleModeCommand 查看或切换当前平台会话的审批模式。
func (h *Handler) handleModeCommand(modeKey string, trimmed string) string {
	return h.handleModeCommandForActor(modeKey, modeKey, trimmed)
}

// handleModeCommandForActor 分离会话状态键和审计操作者，避免群聊路由覆盖真实身份。
func (h *Handler) handleModeCommandForActor(modeKey string, actorUserID string, trimmed string) string {
	fields := strings.Fields(trimmed)
	if len(fields) == 1 {
		if h.isYoloMode(modeKey) {
			return "当前会话审批模式：yolo（当前会话自动同意 Codex 审批请求）。\n发送 /mode default 恢复按钮确认。"
		}
		return "当前会话审批模式：default（Codex 审批请求会弹按钮确认）。\n发送 /mode yolo 改为当前会话自动同意。"
	}
	switch strings.ToLower(strings.TrimSpace(fields[1])) {
	case "yolo":
		h.setYoloMode(modeKey, true)
		h.auditRecord(auditEntry{User: actorUserID, Action: "mode_yolo_enabled"})
		return "已切换为 yolo 模式：当前会话会自动同意 Codex 审批请求。\n⚠️ 该模式不改变全局 sandbox，只跳过本会话按钮确认。发送 /mode default 可恢复确认。"
	case "default":
		h.setYoloMode(modeKey, false)
		return "已切换为 default 模式：Codex 审批请求会弹按钮确认。"
	default:
		return "用法：/mode 查看当前会话审批模式；/mode yolo 当前会话自动同意；/mode default 按钮确认。"
	}
}

func (h *Handler) setYoloMode(modeKey string, on bool) {
	modeKey = strings.TrimSpace(modeKey)
	if modeKey == "" {
		return
	}
	if on {
		h.yoloUsers.Store(modeKey, struct{}{})
		return
	}
	h.yoloUsers.Delete(modeKey)
}

func (h *Handler) isYoloMode(modeKey string) bool {
	_, ok := h.yoloUsers.Load(strings.TrimSpace(modeKey))
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
