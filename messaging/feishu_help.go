package messaging

import (
	"context"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

// handleFeishuHelpCommand 将飞书 /help 升级为分级按钮卡片，保留微信端文本帮助。
func (h *Handler) handleFeishuHelpCommand(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier, routeUserID, command string) bool {
	if msg.Platform != platform.PlatformFeishu || reply == nil || !reply.Capabilities().Buttons {
		return false
	}
	isAdmin := h.isAdminMessage(msg)
	section := feishuHelpSection(command, isAdmin)
	choices := platformChoicesWithMetadata(feishuHelpChoices(section, isAdmin), feishuChoiceSessionMetadata(msg, routeUserID))
	if err := reply.AskChoices(ctx, feishuHelpPrompt(section, isAdmin), choices); err != nil {
		log.Printf("[handler] failed to send feishu help card to %s: %v", msg.UserID, err)
		return false
	}
	return true
}

func feishuHelpSection(command string, isAdmin bool) string {
	fields := strings.Fields(command)
	if len(fields) != 2 || fields[0] != "/help" {
		return ""
	}
	section := strings.ToLower(fields[1])
	switch section {
	case "common", "codex", "claude", "settings":
		return section
	case "admin":
		if isAdmin {
			return section
		}
	}
	return ""
}

func feishuHelpPrompt(section string, isAdmin bool) string {
	titles := map[string]string{
		"common":   "常用与任务",
		"codex":    "Codex",
		"claude":   "Claude",
		"settings": "设置与进度",
		"admin":    "管理员",
	}
	if title := titles[section]; title != "" {
		return "WeClaw 帮助 · " + title + "\n请选择命令，点击后会直接执行。"
	}
	if isAdmin {
		return "WeClaw 帮助\n请选择分类。管理员操作仅对管理员显示。"
	}
	return "WeClaw 帮助\n请选择分类。"
}

func feishuHelpChoices(section string, isAdmin bool) []platform.Choice {
	var choices []platform.Choice
	switch section {
	case "common":
		choices = []platform.Choice{
			{ID: "/status", Label: "运行状态"},
			{ID: "/ps", Label: "运行中任务"},
			{ID: "/stop", Label: "停止当前任务"},
			{ID: "/guide", Label: "暂存下一步指令"},
			{ID: "/cancel", Label: "撤回暂存消息"},
			{ID: "/cwd", Label: "当前工作空间"},
			{ID: "/new", Label: "新建默认会话"},
		}
	case "codex":
		choices = []platform.Choice{
			{ID: "/cx ls", Label: "工作空间与会话"},
			{ID: "/cx status", Label: "会话状态"},
			{ID: "/cx owner", Label: "控制权状态"},
			{ID: "/cx cli", Label: "本地 CLI 接管"},
			{ID: "/cx app", Label: "Codex App 接管"},
			{ID: "/cx quota", Label: "账号额度"},
			{ID: "/cx model ls", Label: "可用模型"},
			{ID: "/cx clean", Label: "清理失效记录"},
			{ID: "/cx help", Label: "完整命令"},
		}
	case "claude":
		choices = []platform.Choice{
			{ID: "/cc ls", Label: "项目与会话"},
			{ID: "/cc status", Label: "会话状态"},
			{ID: "/cc owner", Label: "控制权状态"},
			{ID: "/cc cli", Label: "本地 CLI 接管"},
			{ID: "/cc pwd", Label: "当前项目目录"},
			{ID: "/cc model ls", Label: "可用模型"},
			{ID: "/cc help", Label: "完整命令"},
		}
	case "settings":
		choices = []platform.Choice{
			{ID: "/model", Label: "默认模型"},
			{ID: "/reasoning", Label: "推理强度"},
			{ID: "/mode", Label: "确认模式"},
			{ID: "/progress", Label: "进度模式"},
		}
	case "admin":
		if isAdmin {
			choices = []platform.Choice{
				{ID: "/update", Label: "远程更新"},
				{ID: "/restart", Label: "重启服务"},
				{ID: "/feishu users", Label: "用户管理说明"},
				{ID: "/feishu users pending", Label: "待授权用户"},
				{ID: "/feishu users list", Label: "已授权用户"},
			}
		}
	default:
		choices = []platform.Choice{
			{ID: "/help common", Label: "常用与任务"},
			{ID: "/help codex", Label: "Codex"},
			{ID: "/help claude", Label: "Claude"},
			{ID: "/help settings", Label: "设置与进度"},
		}
		if isAdmin {
			choices = append(choices, platform.Choice{ID: "/help admin", Label: "管理员"})
		}
		return choices
	}
	return append(choices, platform.Choice{ID: "/help", Label: "返回帮助首页"})
}
