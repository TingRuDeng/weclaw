package messaging

import (
	"context"
	"log"

	"github.com/fastclaw-ai/weclaw/platform"
)

// handleFeishuHelpCommand 将飞书 /help 升级为按钮卡片，保留微信端文本帮助。
func (h *Handler) handleFeishuHelpCommand(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) bool {
	if msg.Platform != platform.PlatformFeishu || reply == nil || !reply.Capabilities().Buttons {
		return false
	}
	if err := reply.AskChoices(ctx, feishuHelpPrompt(), feishuHelpChoices()); err != nil {
		log.Printf("[handler] failed to send feishu help card to %s: %v", msg.UserID, err)
		return false
	}
	return true
}

func feishuHelpPrompt() string {
	return "WeClaw 帮助\n请选择常用操作入口。"
}

func feishuHelpChoices() []platform.Choice {
	return []platform.Choice{
		{ID: "/status", Label: "运行状态"},
		{ID: "/cx ls", Label: "Codex 工作空间"},
		{ID: "/cx status", Label: "Codex 会话状态"},
		{ID: "/cx help", Label: "Codex 高级命令"},
		{ID: "/mode", Label: "权限模式"},
		{ID: "/stop", Label: "停止当前任务"},
	}
}
