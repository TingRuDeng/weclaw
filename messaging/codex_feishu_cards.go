package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

// handleFeishuCodexSessionCommand 将飞书侧 Codex 浏览命令升级为按钮卡片，微信仍走文本命令。
func (h *Handler) handleFeishuCodexSessionCommand(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier, trimmed string) bool {
	if msg.Platform != platform.PlatformFeishu || reply == nil || !reply.Capabilities().Buttons {
		return false
	}
	result := h.handleCodexSessionCommand(ctx, msg.UserID, trimmed)
	if h.sendFeishuCodexNavigationChoices(ctx, msg.UserID, reply, trimmed, result) {
		return true
	}
	sendPlatformText(ctx, reply, msg.UserID, result)
	return true
}

func (h *Handler) sendFeishuCodexNavigationChoices(ctx context.Context, userID string, reply platform.Replier, trimmed string, commandReply string) bool {
	agentName, ok := h.codexAgentName()
	if !ok {
		return false
	}
	fields := strings.Fields(trimmed)
	if !isFeishuCodexNavigationCommand(fields) {
		return false
	}
	if isCodexNavigationErrorReply(commandReply) {
		return false
	}
	bindingKey := codexBindingKey(userID, agentName)
	if workspaceRoot, browsing := h.codexBrowseWorkspace(bindingKey); browsing {
		return h.sendFeishuCodexSessionChoices(ctx, userID, reply, bindingKey, workspaceRoot, fields)
	}
	return h.sendFeishuCodexWorkspaceChoices(ctx, userID, reply, bindingKey)
}

func isCodexNavigationErrorReply(reply string) bool {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return false
	}
	errorMarkers := []string{"用法:", "不存在", "失败", "不可用", "不支持", "没有配置"}
	for _, marker := range errorMarkers {
		if strings.Contains(reply, marker) {
			return true
		}
	}
	return false
}

func isFeishuCodexNavigationCommand(fields []string) bool {
	if len(fields) < 2 || !isCodexSessionCommandToken(fields[0]) {
		return false
	}
	if isCodexShortSelectionToken(fields[1]) {
		return true
	}
	switch fields[1] {
	case "ls", "cd":
		return true
	default:
		return false
	}
}

func (h *Handler) sendFeishuCodexWorkspaceChoices(ctx context.Context, userID string, reply platform.Replier, bindingKey string) bool {
	groups := h.codexWorkspaceGroups(bindingKey)
	choices := make([]platform.Choice, 0, len(groups))
	for index, group := range groups {
		if strings.TrimSpace(group.Name) == "" {
			continue
		}
		choices = append(choices, platform.Choice{
			ID:    fmt.Sprintf("/cx cd %d", index),
			Label: group.Name,
		})
	}
	if len(choices) == 0 {
		return false
	}
	return h.askFeishuCodexChoices(ctx, userID, reply, "Codex 工作空间\n请选择要进入的工作空间。", choices)
}

func (h *Handler) sendFeishuCodexSessionChoices(ctx context.Context, userID string, reply platform.Replier, bindingKey string, workspaceRoot string, fields []string) bool {
	sessions := h.codexSessionsForWorkspace(bindingKey, workspaceRoot)
	choices := make([]platform.Choice, 0, len(sessions))
	for index, session := range sessions {
		if strings.TrimSpace(session.ThreadID) == "" || session.PendingNewThread {
			continue
		}
		choices = append(choices, platform.Choice{
			ID:    fmt.Sprintf("/cx switch %d", index),
			Label: codexSessionDisplayName(session),
		})
	}
	if len(choices) == 0 || !shouldShowFeishuSessionChoices(fields, len(choices)) {
		return false
	}
	prompt := fmt.Sprintf("%s 会话\n请选择要切换的会话。", shortCodexWorkspaceName(workspaceRoot))
	return h.askFeishuCodexChoices(ctx, userID, reply, prompt, choices)
}

func shouldShowFeishuSessionChoices(fields []string, choiceCount int) bool {
	if choiceCount > 1 {
		return true
	}
	return len(fields) >= 2 && fields[1] == "ls"
}

func (h *Handler) askFeishuCodexChoices(ctx context.Context, userID string, reply platform.Replier, prompt string, choices []platform.Choice) bool {
	if err := reply.AskChoices(ctx, prompt, choices); err != nil {
		log.Printf("[handler] failed to send feishu codex choices to %s: %v", userID, err)
		return false
	}
	return true
}
