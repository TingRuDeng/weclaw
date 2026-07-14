package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

type feishuCodexSessionCommandRequest struct {
	ctx         context.Context
	message     platform.IncomingMessage
	routeUserID string
	reply       platform.Replier
	trimmed     string
	result      navigationCommandResult
}

type feishuCodexChoiceRequest struct {
	ctx           context.Context
	userID        string
	reply         platform.Replier
	bindingKey    string
	workspaceRoot string
	fields        []string
	admin         bool
	metadata      map[string]string
}

type feishuCodexChoicePrompt struct {
	ctx     context.Context
	userID  string
	reply   platform.Replier
	prompt  string
	choices []platform.Choice
}

// handleFeishuCodexSessionCommand 将飞书侧 Codex 浏览命令升级为按钮卡片，微信仍走文本命令。
func (h *Handler) handleFeishuCodexSessionCommand(req feishuCodexSessionCommandRequest) bool {
	ctx, msg := req.ctx, req.message
	routeUserID, reply, trimmed := req.routeUserID, req.reply, req.trimmed
	if msg.Platform != platform.PlatformFeishu || reply == nil || !reply.Capabilities().Buttons {
		return false
	}
	if notice, blocked := h.runningCodexNavigationNotice(req); blocked {
		sendPlatformText(ctx, reply, msg.UserID, notice)
		return true
	}
	result := h.handleCodexSessionCommandForRouteResult(ctx, codexSessionCommandRequest{
		ActorUserID: msg.UserID,
		RouteUserID: routeUserID,
		Trimmed:     trimmed,
		Platform:    msg.Platform,
		AccountID:   msg.AccountID,
		Reply:       reply,
		Admin:       h.isAdminMessage(msg),
	})
	req.result = result
	if h.sendFeishuCodexOwnerChoices(req) {
		return true
	}
	if h.sendFeishuCodexNavigationChoices(req) {
		return true
	}
	sendPlatformText(ctx, reply, msg.UserID, result.Reply)
	return true
}

// sendFeishuCodexOwnerChoices 把显式控制权移交呈现为二选一卡片。
func (h *Handler) sendFeishuCodexOwnerChoices(req feishuCodexSessionCommandRequest) bool {
	if !req.result.ShowCard || !isFeishuCodexOwnerCommand(strings.Fields(req.trimmed)) {
		return false
	}
	choices := []platform.Choice{
		{ID: "/cx owner remote", Label: "交给当前远程窗口"},
		{ID: "/cx owner desktop", Label: "交给 Codex Desktop"},
	}
	metadata := feishuChoiceSessionMetadata(req.message, req.routeUserID)
	choices = platformChoicesWithMetadata(choices, metadata)
	return h.askFeishuCodexChoices(feishuCodexChoicePrompt{
		ctx: req.ctx, userID: req.message.UserID, reply: req.reply,
		prompt: req.result.Reply, choices: choices,
	})
}

// isFeishuCodexOwnerCommand 只让状态命令生成选择卡，移交结果直接返回确认文本。
func isFeishuCodexOwnerCommand(fields []string) bool {
	return len(fields) == 2 && isCodexSessionCommandToken(fields[0]) && fields[1] == "owner"
}

func (h *Handler) runningCodexNavigationNotice(req feishuCodexSessionCommandRequest) (string, bool) {
	if !isFeishuCodexNavigationCommand(strings.Fields(req.trimmed)) {
		return "", false
	}
	_, _, key, err := h.codexGuideTargetForRoute(req.ctx, req.message.UserID, req.routeUserID)
	if err != nil {
		return "", false
	}
	_, ok := h.activeTask(key)
	if !ok {
		return "", false
	}
	return runningCodexNavigationBlockedPrompt(), true
}

func runningCodexNavigationBlockedPrompt() string {
	return "当前任务正在执行，请在完成后再发送 /cx ls。"
}

func (h *Handler) sendFeishuCodexNavigationChoices(req feishuCodexSessionCommandRequest) bool {
	agentName, ok := h.codexAgentName()
	if !ok {
		return false
	}
	fields := strings.Fields(req.trimmed)
	if !isFeishuCodexNavigationCommand(fields) {
		return false
	}
	if !req.result.ShowCard {
		return false
	}
	bindingKey := codexBindingKey(req.routeUserID, agentName)
	metadata := feishuChoiceSessionMetadata(req.message, req.routeUserID)
	choiceReq := feishuCodexChoiceRequest{
		ctx: req.ctx, userID: req.message.UserID, reply: req.reply,
		bindingKey: bindingKey, fields: fields,
		admin: h.isAdminMessage(req.message), metadata: metadata,
	}
	if workspaceRoot, browsing := h.codexBrowseWorkspace(bindingKey); browsing {
		choiceReq.workspaceRoot = workspaceRoot
		return h.sendFeishuCodexSessionChoices(choiceReq)
	}
	return h.sendFeishuCodexWorkspaceChoices(choiceReq)
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

func (h *Handler) sendFeishuCodexWorkspaceChoices(req feishuCodexChoiceRequest) bool {
	groups := h.codexWorkspaceGroupsForAccess(req.bindingKey, req.userID, req.admin)
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
	choices = platformChoicesWithMetadata(choices, req.metadata)
	return h.askFeishuCodexChoices(feishuCodexChoicePrompt{
		ctx: req.ctx, userID: req.userID, reply: req.reply,
		prompt: "Codex 工作空间\n请选择要进入的工作空间。", choices: choices,
	})
}

func (h *Handler) sendFeishuCodexSessionChoices(req feishuCodexChoiceRequest) bool {
	sessions := h.codexSessionsForWorkspace(req.bindingKey, req.workspaceRoot)
	choices := make([]platform.Choice, 0, len(sessions))
	for _, session := range sessions {
		if strings.TrimSpace(session.ThreadID) == "" || session.PendingNewThread {
			continue
		}
		choices = append(choices, platform.Choice{
			ID:    fmt.Sprintf("/cx switch %s", strings.TrimSpace(session.ThreadID)),
			Label: codexSessionDisplayName(session),
		})
	}
	sessionChoiceCount := len(choices)
	if sessionChoiceCount == 0 || !shouldShowFeishuSessionChoices(req.fields, sessionChoiceCount) {
		return false
	}
	choices = append(choices, platform.Choice{
		ID:    "/cx cd ..",
		Label: "返回工作空间列表",
	})
	choices = platformChoicesWithMetadata(choices, req.metadata)
	prompt := fmt.Sprintf("%s 会话\n请选择要切换的会话。", shortCodexWorkspaceName(req.workspaceRoot))
	return h.askFeishuCodexChoices(feishuCodexChoicePrompt{
		ctx: req.ctx, userID: req.userID, reply: req.reply, prompt: prompt, choices: choices,
	})
}

func shouldShowFeishuSessionChoices(fields []string, choiceCount int) bool {
	if choiceCount > 1 {
		return true
	}
	if len(fields) < 2 {
		return false
	}
	return fields[1] == "ls" || fields[1] == "cd"
}

func (h *Handler) askFeishuCodexChoices(req feishuCodexChoicePrompt) bool {
	if err := req.reply.AskChoices(req.ctx, req.prompt, req.choices); err != nil {
		log.Printf("[handler] failed to send feishu codex choices to %s: %v", req.userID, err)
		return false
	}
	return true
}
