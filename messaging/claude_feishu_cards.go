package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

type claudeFeishuCommandRequest struct {
	Context     context.Context
	Message     platform.IncomingMessage
	RouteUserID string
	Reply       platform.Replier
	Trimmed     string
}

type claudeFeishuChoiceRequest struct {
	Context       context.Context
	Reply         platform.Replier
	Route         claudeSessionRoute
	Metadata      map[string]string
	WorkspaceRoot string
}

type claudeChoiceCard struct {
	Prompt  string
	Choices []platform.Choice
	Meta    map[string]string
}

// handleFeishuClaudeSessionCommand 将飞书侧 Claude 导航命令渲染为两级按钮卡片。
func (h *Handler) handleFeishuClaudeSessionCommand(req claudeFeishuCommandRequest) bool {
	msg := req.Message
	if msg.Platform != platform.PlatformFeishu || req.Reply == nil || !req.Reply.Capabilities().Buttons {
		return false
	}
	if notice, blocked := h.runningClaudeNavigationNotice(req); blocked {
		sendPlatformText(req.Context, req.Reply, msg.UserID, notice)
		return true
	}
	result := h.handleClaudeSessionCommandForRoute(req.Context, msg.UserID, req.RouteUserID, h.isAdminMessage(msg), req.Trimmed)
	if h.sendFeishuClaudeNavigationChoices(req, result) {
		return true
	}
	sendPlatformText(req.Context, req.Reply, msg.UserID, result)
	return true
}

// runningClaudeNavigationNotice 使用消息执行入口的同一任务键阻止运行中插入卡片。
func (h *Handler) runningClaudeNavigationNotice(req claudeFeishuCommandRequest) (string, bool) {
	if !isFeishuClaudeNavigationCommand(strings.Fields(req.Trimmed)) {
		return "", false
	}
	agentName, ag, err := h.getClaudeSessionAgent(req.Context)
	if err != nil {
		return "", false
	}
	key := h.agentExecutionKeyForRoute(req.Message.UserID, req.RouteUserID, agentName, ag)
	if _, ok := h.activeTask(key); !ok {
		return "", false
	}
	return "当前任务正在执行，请在完成后再发送 /cc ls。", true
}

// sendFeishuClaudeNavigationChoices 根据命令层级发送工作空间或会话卡片。
func (h *Handler) sendFeishuClaudeNavigationChoices(req claudeFeishuCommandRequest, commandReply string) bool {
	fields := strings.Fields(req.Trimmed)
	if !isFeishuClaudeNavigationCommand(fields) || isCodexNavigationErrorReply(commandReply) {
		return false
	}
	agentName, ok := h.claudeAgentName()
	if !ok {
		return false
	}
	msg := req.Message
	route := claudeSessionRoute{ActorUserID: msg.UserID, UserID: req.RouteUserID, BindingKey: claudeBindingKey(req.RouteUserID, agentName), Admin: h.isAdminMessage(msg)}
	choiceReq := claudeFeishuChoiceRequest{Context: req.Context, Reply: req.Reply, Route: route, Metadata: feishuChoiceSessionMetadata(msg, req.RouteUserID)}
	if fields[1] == "ls" || fields[2] == ".." {
		return h.sendFeishuClaudeWorkspaceChoices(choiceReq)
	}
	workspaceRoot, ok := h.ensureClaudeSessions().getActiveWorkspace(route.BindingKey)
	if !ok {
		return false
	}
	choiceReq.WorkspaceRoot = workspaceRoot
	return h.sendFeishuClaudeSessionChoices(choiceReq)
}

// isFeishuClaudeNavigationCommand 只接受完整的 `/cc ls` 和 `/cc cd` 导航命令。
func isFeishuClaudeNavigationCommand(fields []string) bool {
	if len(fields) < 2 || !isClaudeSessionCommandToken(fields[0]) {
		return false
	}
	return fields[1] == "ls" || (fields[1] == "cd" && len(fields) == 3)
}

// sendFeishuClaudeWorkspaceChoices 将权限过滤后的工作空间映射为稳定编号按钮。
func (h *Handler) sendFeishuClaudeWorkspaceChoices(req claudeFeishuChoiceRequest) bool {
	groups := h.claudeWorkspaceGroupsForAccess(req.Route.BindingKey, req.Route.ActorUserID, req.Route.Admin)
	choices := make([]platform.Choice, 0, len(groups))
	for index, group := range groups {
		choices = append(choices, platform.Choice{ID: fmt.Sprintf("/cc cd %d", index), Label: group.Name})
	}
	card := claudeChoiceCard{Prompt: "Claude 工作空间\n请选择要进入的工作空间。", Choices: choices, Meta: req.Metadata}
	return h.askFeishuClaudeChoices(req.Context, req.Reply, card)
}

// sendFeishuClaudeSessionChoices 使用稳定 sessionId 构造会话切换按钮。
func (h *Handler) sendFeishuClaudeSessionChoices(req claudeFeishuChoiceRequest) bool {
	sessions := h.claudeSessionsForWorkspace(req.Route, req.WorkspaceRoot)
	choices := make([]platform.Choice, 0, len(sessions)+1)
	for _, session := range sessions {
		choices = append(choices, platform.Choice{ID: "/cc switch " + strings.TrimSpace(session.ThreadID), Label: codexSessionDisplayName(session)})
	}
	if len(choices) == 0 {
		return false
	}
	choices = append(choices, platform.Choice{ID: "/cc cd ..", Label: "返回工作空间列表"})
	prompt := fmt.Sprintf("%s 会话\n请选择要切换的会话。", shortCodexWorkspaceName(req.WorkspaceRoot))
	return h.askFeishuClaudeChoices(req.Context, req.Reply, claudeChoiceCard{Prompt: prompt, Choices: choices, Meta: req.Metadata})
}

// askFeishuClaudeChoices 统一附加飞书会话路由并发送选择卡片。
func (h *Handler) askFeishuClaudeChoices(ctx context.Context, reply platform.Replier, card claudeChoiceCard) bool {
	if len(card.Choices) == 0 {
		return false
	}
	if err := reply.AskChoices(ctx, card.Prompt, platformChoicesWithMetadata(card.Choices, card.Meta)); err != nil {
		log.Printf("[handler] failed to send feishu claude choices: %v", err)
		return false
	}
	return true
}
