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
	AccountID     string
	Reply         platform.Replier
	Route         claudeSessionRoute
	Metadata      map[string]string
	WorkspaceRoot string
	Page          int
	Snapshot      string
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
	fields := strings.Fields(req.Trimmed)
	if isLegacyFeishuWorkspaceChoice(msg, req.Trimmed) {
		sendPlatformText(req.Context, req.Reply, msg.UserID, "工作空间卡片已过期，请重新发送 /cc ls。")
		return true
	}
	_, paginated := parseFeishuNavigationPage(fields, "/cc")
	result := cardNavigationResult("当前导航状态已变化，请发送 /cc ls 重新打开。")
	if !paginated {
		result = h.handleClaudeSessionCommandForRouteResult(req.Context, msg.UserID, req.RouteUserID, h.isAdminMessage(msg), req.Trimmed)
	}
	if h.sendFeishuClaudeNavigationChoices(req, result) {
		return true
	}
	sendPlatformText(req.Context, req.Reply, msg.UserID, result.Reply)
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
func (h *Handler) sendFeishuClaudeNavigationChoices(req claudeFeishuCommandRequest, result navigationCommandResult) bool {
	fields := strings.Fields(req.Trimmed)
	if !isFeishuClaudeNavigationCommand(fields) || !result.ShowCard {
		return false
	}
	agentName, ag, err := h.getClaudeSessionAgent(req.Context)
	if err != nil {
		return false
	}
	msg := req.Message
	workspaceRoot := h.claudeWorkspaceRootForUser(req.RouteUserID, agentName, ag)
	route := claudeSessionRoute{
		Context: req.Context, ActorUserID: msg.UserID, UserID: req.RouteUserID,
		AgentName: agentName, Agent: ag, WorkspaceRoot: workspaceRoot,
		BindingKey: claudeBindingKey(req.RouteUserID, agentName), Admin: h.isAdminMessage(msg),
	}
	choiceReq := claudeFeishuChoiceRequest{
		Context: req.Context, AccountID: msg.AccountID, Reply: req.Reply, Route: route,
		Metadata: feishuChoiceSessionMetadata(msg, req.RouteUserID), Snapshot: feishuNavigationSnapshotFromMessage(msg),
	}
	if page, ok := parseFeishuNavigationPage(fields, "/cc"); ok {
		choiceReq.Page = page.Page
		if page.Kind == "workspaces" {
			return h.sendFeishuClaudeWorkspaceChoices(choiceReq)
		}
		choiceReq.WorkspaceRoot = workspaceRoot
		return h.sendFeishuClaudeSessionChoices(choiceReq)
	}
	if fields[1] == "ls" || fields[2] == ".." {
		return h.sendFeishuClaudeWorkspaceChoices(choiceReq)
	}
	choiceReq.WorkspaceRoot = workspaceRoot
	return h.sendFeishuClaudeSessionChoices(choiceReq)
}

// isFeishuClaudeNavigationCommand 只接受完整的 `/cc ls` 和 `/cc cd` 导航命令。
func isFeishuClaudeNavigationCommand(fields []string) bool {
	if len(fields) < 2 || !isClaudeSessionCommandToken(fields[0]) {
		return false
	}
	if fields[1] == "ls" || (fields[1] == "cd" && len(fields) == 3) {
		return true
	}
	_, ok := parseFeishuNavigationPage(fields, "/cc")
	return ok
}

// sendFeishuClaudeWorkspaceChoices 将权限过滤后的工作空间映射为短期 opaque token 按钮。
func (h *Handler) sendFeishuClaudeWorkspaceChoices(req claudeFeishuChoiceRequest) bool {
	scope := feishuNavigationSnapshotScope{
		AccountID: req.AccountID, ActorUserID: req.Route.ActorUserID, BindingKey: req.Route.BindingKey,
		AgentKind: feishuWorkspaceChoiceClaude, Section: feishuNavigationSectionWorkspaces,
	}
	choices, snapshot, ok := h.loadFeishuClaudeWorkspaceSnapshot(req, scope)
	if !ok {
		return false
	}
	choices, page := paginateFeishuChoices(choices, req.Page)
	for index := range choices {
		token := h.feishuWorkspaceChoices.issue(
			feishuWorkspaceChoiceClaude, req.Route.ActorUserID, req.Route.BindingKey, normalizeClaudeWorkspaceRoot(choices[index].ID),
		)
		choices[index].ID = "/cc cd " + token
	}
	choices = appendFeishuPageNavigation(choices, "/cc", "workspaces", page, snapshot)
	card := claudeChoiceCard{
		Prompt:  feishuPaginatedPrompt("Claude 工作空间\n请选择要进入的工作空间。", page),
		Choices: choices, Meta: req.Metadata,
	}
	return h.askFeishuClaudeChoices(req.Context, req.Reply, card)
}

func (h *Handler) loadFeishuClaudeWorkspaceSnapshot(req claudeFeishuChoiceRequest, scope feishuNavigationSnapshotScope) ([]platform.Choice, string, bool) {
	if req.Page > 0 && req.Snapshot != "" {
		choices, ok := h.feishuNavSnapshots.load(req.Snapshot, scope)
		return choices, req.Snapshot, ok
	}
	groups, err := h.claudeWorkspaceGroupsForRoute(req.Route)
	if err != nil {
		return nil, "", false
	}
	choices := make([]platform.Choice, 0, len(groups))
	for _, group := range groups {
		choices = append(choices, platform.Choice{
			ID: normalizeClaudeWorkspaceRoot(group.Root), Label: claudeWorkspaceGroupLabel(group),
		})
	}
	if len(choices) == 0 {
		return nil, "", false
	}
	snapshot := h.feishuNavSnapshots.issue(scope, choices)
	return choices, snapshot, true
}

// sendFeishuClaudeSessionChoices 使用稳定 sessionId 构造会话切换按钮。
func (h *Handler) sendFeishuClaudeSessionChoices(req claudeFeishuChoiceRequest) bool {
	scope := feishuNavigationSnapshotScope{
		AccountID: req.AccountID, ActorUserID: req.Route.ActorUserID, BindingKey: req.Route.BindingKey,
		AgentKind: feishuWorkspaceChoiceClaude, Section: feishuNavigationSectionSessions,
		WorkspaceRoot: normalizeClaudeWorkspaceRoot(req.WorkspaceRoot),
	}
	choices, snapshot, ok := h.loadFeishuClaudeSessionSnapshot(req, scope)
	if !ok {
		return false
	}
	if len(choices) == 0 {
		return false
	}
	choices, page := paginateFeishuChoices(choices, req.Page)
	choices = appendFeishuPageNavigation(choices, "/cc", "sessions", page, snapshot)
	choices = append(choices, feishuNavigationChoice("/cc cd ..", "← 返回上一级"))
	prompt := feishuPaginatedPrompt(
		fmt.Sprintf("%s 会话\n请选择要切换的会话。", shortCodexWorkspaceName(req.WorkspaceRoot)), page,
	)
	return h.askFeishuClaudeChoices(req.Context, req.Reply, claudeChoiceCard{Prompt: prompt, Choices: choices, Meta: req.Metadata})
}

func (h *Handler) loadFeishuClaudeSessionSnapshot(req claudeFeishuChoiceRequest, scope feishuNavigationSnapshotScope) ([]platform.Choice, string, bool) {
	if req.Page > 0 && req.Snapshot != "" {
		choices, ok := h.feishuNavSnapshots.load(req.Snapshot, scope)
		return choices, req.Snapshot, ok
	}
	sessions, err := h.claudeSessionsForWorkspace(req.Route, req.WorkspaceRoot)
	if err != nil {
		return nil, "", false
	}
	choices := make([]platform.Choice, 0, len(sessions))
	for _, session := range sessions {
		choices = append(choices, platform.Choice{
			ID: "/cc switch " + strings.TrimSpace(session.ThreadID), Label: codexSessionDisplayName(session),
		})
	}
	if len(choices) == 0 {
		return nil, "", false
	}
	snapshot := h.feishuNavSnapshots.issue(scope, choices)
	return choices, snapshot, true
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
