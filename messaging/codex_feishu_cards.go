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
	page        feishuNavigationPageRequest
}

type feishuCodexChoiceRequest struct {
	ctx           context.Context
	accountID     string
	userID        string
	reply         platform.Replier
	bindingKey    string
	workspaceRoot string
	fields        []string
	admin         bool
	metadata      map[string]string
	page          int
	snapshot      string
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
	fields := strings.Fields(trimmed)
	if isLegacyFeishuWorkspaceChoice(msg, trimmed) {
		sendPlatformText(ctx, reply, msg.UserID, "工作空间卡片已过期，请重新发送 /cx ls。")
		return true
	}
	if page, ok := parseFeishuNavigationPage(fields, "/cx"); ok {
		req.page = page
		req.result = cardNavigationResult("当前导航状态已变化，请发送 /cx ls 重新打开。")
	} else {
		req.result = h.handleCodexSessionCommandForRouteResult(ctx, codexSessionCommandRequest{
			ActorUserID: msg.UserID,
			RouteUserID: routeUserID,
			Trimmed:     trimmed,
			Platform:    msg.Platform,
			AccountID:   msg.AccountID,
			Reply:       reply,
			Admin:       h.isAdminMessage(msg),
		})
	}
	if h.sendFeishuCodexNavigationChoices(req) {
		return true
	}
	sendPlatformText(ctx, reply, msg.UserID, req.result.Reply)
	return true
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
		ctx: req.ctx, accountID: req.message.AccountID, userID: req.message.UserID, reply: req.reply,
		bindingKey: bindingKey, fields: fields,
		admin: h.isAdminMessage(req.message), metadata: metadata, page: req.page.Page,
		snapshot: feishuNavigationSnapshotFromMessage(req.message),
	}
	if req.page.Kind == "workspaces" {
		return h.sendFeishuCodexWorkspaceChoices(choiceReq)
	}
	if req.page.Kind == "sessions" {
		workspaceRoot, browsing := h.codexBrowseWorkspace(bindingKey)
		if !browsing {
			return false
		}
		choiceReq.workspaceRoot = workspaceRoot
		return h.sendFeishuCodexSessionChoices(choiceReq)
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
	case "page":
		_, ok := parseFeishuNavigationPage(fields, "/cx")
		return ok
	default:
		return false
	}
}

func (h *Handler) sendFeishuCodexWorkspaceChoices(req feishuCodexChoiceRequest) bool {
	scope := feishuNavigationSnapshotScope{
		AccountID: req.accountID, ActorUserID: req.userID, BindingKey: req.bindingKey,
		AgentKind: feishuWorkspaceChoiceCodex, Section: feishuNavigationSectionWorkspaces,
	}
	choices, snapshot, ok := h.loadFeishuCodexWorkspaceSnapshot(req, scope)
	if !ok {
		return false
	}
	if len(choices) == 0 {
		return false
	}
	choices, page := paginateFeishuChoices(choices, req.page)
	for index := range choices {
		token := h.feishuWorkspaceChoices.issue(
			feishuWorkspaceChoiceCodex, req.userID, req.bindingKey, normalizeCodexWorkspaceRoot(choices[index].ID),
		)
		choices[index].ID = "/cx cd " + token
	}
	choices = appendFeishuPageNavigation(choices, "/cx", "workspaces", page, snapshot)
	choices = platformChoicesWithMetadata(choices, req.metadata)
	return h.askFeishuCodexChoices(feishuCodexChoicePrompt{
		ctx: req.ctx, userID: req.userID, reply: req.reply,
		prompt: feishuPaginatedPrompt("Codex 工作空间\n请选择要进入的工作空间。", page), choices: choices,
	})
}

func (h *Handler) loadFeishuCodexWorkspaceSnapshot(req feishuCodexChoiceRequest, scope feishuNavigationSnapshotScope) ([]platform.Choice, string, bool) {
	if req.page > 0 && req.snapshot != "" {
		choices, ok := h.feishuNavSnapshots.load(req.snapshot, scope)
		return choices, req.snapshot, ok
	}
	groups := h.codexWorkspaceGroupsForAccess(req.bindingKey, req.userID, req.admin)
	choices := make([]platform.Choice, 0, len(groups))
	for _, group := range groups {
		if name := strings.TrimSpace(group.Name); name != "" {
			choices = append(choices, platform.Choice{ID: normalizeCodexWorkspaceRoot(group.Root), Label: name})
		}
	}
	if len(choices) == 0 {
		return nil, "", false
	}
	snapshot := h.feishuNavSnapshots.issue(scope, choices)
	return choices, snapshot, true
}

func (h *Handler) sendFeishuCodexSessionChoices(req feishuCodexChoiceRequest) bool {
	scope := feishuNavigationSnapshotScope{
		AccountID: req.accountID, ActorUserID: req.userID, BindingKey: req.bindingKey,
		AgentKind: feishuWorkspaceChoiceCodex, Section: feishuNavigationSectionSessions,
		WorkspaceRoot: normalizeCodexWorkspaceRoot(req.workspaceRoot),
	}
	choices, snapshot, ok := h.loadFeishuCodexSessionSnapshot(req, scope)
	if !ok {
		return false
	}
	sessionChoiceCount := len(choices)
	if sessionChoiceCount == 0 || !shouldShowFeishuSessionChoices(req.fields, sessionChoiceCount) {
		return false
	}
	choices, page := paginateFeishuChoices(choices, req.page)
	choices = appendFeishuPageNavigation(choices, "/cx", "sessions", page, snapshot)
	choices = append(choices, feishuNavigationChoice("/cx cd ..", "← 返回上一级"))
	choices = platformChoicesWithMetadata(choices, req.metadata)
	prompt := feishuPaginatedPrompt(
		fmt.Sprintf("%s 会话\n请选择要切换的会话。", shortCodexWorkspaceName(req.workspaceRoot)), page,
	)
	return h.askFeishuCodexChoices(feishuCodexChoicePrompt{
		ctx: req.ctx, userID: req.userID, reply: req.reply, prompt: prompt, choices: choices,
	})
}

func (h *Handler) loadFeishuCodexSessionSnapshot(req feishuCodexChoiceRequest, scope feishuNavigationSnapshotScope) ([]platform.Choice, string, bool) {
	if req.page > 0 && req.snapshot != "" {
		choices, ok := h.feishuNavSnapshots.load(req.snapshot, scope)
		return choices, req.snapshot, ok
	}
	sessions := h.codexSessionsForWorkspace(req.bindingKey, req.workspaceRoot)
	choices := make([]platform.Choice, 0, len(sessions))
	for _, session := range sessions {
		if strings.TrimSpace(session.ThreadID) == "" || session.PendingNewThread {
			continue
		}
		choices = append(choices, platform.Choice{
			ID: fmt.Sprintf("/cx switch %s", strings.TrimSpace(session.ThreadID)), Label: codexSessionDisplayName(session),
		})
	}
	if len(choices) == 0 {
		return nil, "", false
	}
	snapshot := h.feishuNavSnapshots.issue(scope, choices)
	return choices, snapshot, true
}

func shouldShowFeishuSessionChoices(fields []string, choiceCount int) bool {
	if choiceCount > 1 {
		return true
	}
	if len(fields) < 2 {
		return false
	}
	return fields[1] == "ls" || fields[1] == "cd" || fields[1] == "page"
}

func (h *Handler) askFeishuCodexChoices(req feishuCodexChoicePrompt) bool {
	if err := req.reply.AskChoices(req.ctx, req.prompt, req.choices); err != nil {
		log.Printf("[handler] failed to send feishu codex choices to %s: %v", req.userID, err)
		return false
	}
	return true
}
