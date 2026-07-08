package messaging

import (
	"context"
	"log"

	"github.com/fastclaw-ai/weclaw/platform"
)

// handleFeishuHelpCommand 将飞书 /help 升级为按钮卡片，保留微信端文本帮助。
func (h *Handler) handleFeishuHelpCommand(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier, routeUserID string) bool {
	if msg.Platform != platform.PlatformFeishu || reply == nil || !reply.Capabilities().Buttons {
		return false
	}
	isAdmin := h.isAdminMessage(msg)
	choices := platformChoicesWithMetadata(feishuHelpChoices(isAdmin), feishuChoiceSessionMetadata(msg, routeUserID))
	if err := reply.AskChoices(ctx, feishuHelpPrompt(isAdmin), choices); err != nil {
		log.Printf("[handler] failed to send feishu help card to %s: %v", msg.UserID, err)
		return false
	}
	return true
}

func feishuHelpPrompt(isAdmin bool) string {
	if !isAdmin {
		return "WeClaw 帮助\n请选择常用操作入口。"
	}
	return "WeClaw 帮助\n请选择常用操作入口。"
}

func feishuHelpChoices(isAdmin bool) []platform.Choice {
	choices := []platform.Choice{
		{ID: "/status", Label: "运行状态"},
		{ID: "/cx ls", Label: "Codex 工作空间"},
		{ID: "/cx status", Label: "Codex 会话状态"},
		{ID: "/cx help", Label: "Codex 高级命令"},
		{ID: "/mode", Label: "确认模式"},
		{ID: "/stop", Label: "停止当前任务"},
	}
	if !isAdmin {
		return choices
	}
	return append(choices,
		platform.Choice{ID: "/update", Label: "远程更新"},
		platform.Choice{ID: "/restart", Label: "重启服务"},
		platform.Choice{ID: "/feishu users pending", Label: "待授权用户"},
		platform.Choice{ID: "/feishu users list", Label: "已授权用户"},
	)
}
