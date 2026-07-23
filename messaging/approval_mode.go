package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// handleModePlatformCommand 为飞书无参数 /mode 提供选择卡；共享窗口按真实操作者隔离 YOLO。
func (h *Handler) handleModePlatformCommand(ctx context.Context, req platformCommandRequest, routeUserID string) bool {
	modeKey := approvalModeKey(req.Message.UserID, routeUserID)
	if req.Trimmed == "/mode" && h.sendFeishuModeCard(ctx, req, routeUserID, modeKey) {
		return true
	}
	replyPlatformCommand(ctx, req, h.handleModeCommandForActor(routeUserID, req.Message.UserID, req.Trimmed))
	return true
}

func (h *Handler) sendFeishuModeCard(ctx context.Context, req platformCommandRequest, routeUserID string, modeKey string) bool {
	msg := req.Message
	if msg.Platform != platform.PlatformFeishu || req.Reply == nil || !req.Reply.Capabilities().Buttons {
		return false
	}
	yolo := h.isYoloMode(modeKey)
	current := "default"
	if yolo {
		current = "yolo"
	}
	choices := []platform.Choice{
		{ID: "/mode default", Label: markCurrentChoice("default · 每次按钮确认", !yolo)},
		{ID: "/mode yolo", Label: markCurrentChoice("yolo · 自动同意审批", yolo)},
	}
	choices = platformChoicesWithMetadata(choices, feishuChoiceSessionMetadata(msg, routeUserID))
	prompt := "当前审批模式：" + current + "\n\n请选择 Agent 授权处理方式。群聊中的 yolo 只对当前操作者生效；它不改变全局 sandbox，Agent 的普通提问仍会显示选择卡片。"
	if err := req.Reply.AskChoices(ctx, prompt, choices); err != nil {
		log.Printf("[handler] failed to send feishu mode choices: %v", err)
		return false
	}
	return true
}

// handleModeCommand 查看或切换当前平台会话的审批模式。
func (h *Handler) handleModeCommand(routeUserID string, trimmed string) string {
	return h.handleModeCommandForActor(routeUserID, routeUserID, trimmed)
}

// handleModeCommandForActor 分离会话状态键和审计操作者，避免群聊路由覆盖真实身份。
func (h *Handler) handleModeCommandForActor(routeUserID string, actorUserID string, trimmed string) string {
	modeKey := approvalModeKey(actorUserID, routeUserID)
	fields := strings.Fields(trimmed)
	if len(fields) == 1 {
		if h.isYoloMode(modeKey) {
			return "当前审批模式：yolo（当前操作者在此窗口自动同意 Agent 授权请求；普通提问仍需选择）。\n发送 /mode default 恢复授权确认。"
		}
		return "当前审批模式：default（Agent 授权请求会弹按钮确认）。\n发送 /mode yolo 改为当前操作者在此窗口自动同意授权。"
	}
	switch strings.ToLower(strings.TrimSpace(fields[1])) {
	case "yolo":
		h.setYoloMode(modeKey, true)
		resolved := h.resolvePendingApprovalsForYolo(actorUserID, routeUserID)
		h.auditRecord(auditEntry{User: actorUserID, Action: "mode_yolo_enabled"})
		reply := "已切换为 yolo 模式：当前操作者在此窗口会自动同意 Agent 授权请求。\n⚠️ 该模式不改变全局 sandbox；Agent 的普通提问仍需选择。发送 /mode default 可恢复授权确认。"
		if resolved > 0 {
			reply += fmt.Sprintf("\n已同时放行 %d 个切换前待确认的授权请求。", resolved)
		}
		return reply
	case "default":
		h.setYoloMode(modeKey, false)
		return "已切换为 default 模式：Agent 授权请求会弹按钮确认。"
	default:
		return "用法：/mode 查看当前审批模式；/mode yolo 当前操作者在此窗口自动同意；/mode default 按钮确认。"
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

func approvalModeKey(actorUserID string, routeUserID string) string {
	actorUserID = strings.TrimSpace(actorUserID)
	routeUserID = strings.TrimSpace(routeUserID)
	switch {
	case routeUserID == "":
		return actorUserID
	case actorUserID == "", actorUserID == routeUserID:
		return routeUserID
	default:
		return routeUserID + "\x00actor:" + actorUserID
	}
}

// autoApproveApprovalOption 在 yolo 模式下只选择显式允许项，避免把拒绝/取消误当作自动同意。
func autoApproveApprovalOption(options []agent.ApprovalOption) string {
	for _, option := range options {
		if option.Kind == "allow" && strings.TrimSpace(option.ID) != "" {
			return option.ID
		}
	}
	return ""
}
