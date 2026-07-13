package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// switchDefault 切换当前消息会话的默认 Agent，并在目标 Agent 可用后持久化选择。
func (h *Handler) switchDefault(ctx context.Context, routeUserID string, name string) string {
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to switch default to %q: %v", name, err)
		return fmt.Sprintf("切换到 %q 失败：%v", name, err)
	}
	if err := h.ensureAgentSessions().Set(routeUserID, name); err != nil {
		log.Printf("[handler] failed to save session agent %q for %q: %v", name, routeUserID, err)
		return fmt.Sprintf("切换到 %q 失败：%v", name, err)
	}

	info := ag.Info()
	log.Printf("[handler] switched session %q to agent %s (%s)", routeUserID, name, info)
	return fmt.Sprintf("当前会话已切换到 %s", name)
}

type defaultSessionResetRequest struct {
	actorUserID string
	routeUserID string
	platform    platform.PlatformName
	accountID   string
}

// resetDefaultSessionForMessage 按消息会话选择的 Agent 重置对应会话。
func (h *Handler) resetDefaultSessionForMessage(ctx context.Context, req defaultSessionResetRequest) string {
	actorUserID := req.actorUserID
	routeUserID := req.routeUserID
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = actorUserID
	}
	name := h.defaultAgentNameForRoute(routeUserID, req.platform, req.accountID)
	ag, err := h.getAgent(ctx, name)
	if err != nil || ag == nil {
		return "No agent running."
	}
	if isCodexAgent(name, ag.Info()) {
		return h.resetDefaultCodexSessionForRoute(ctx, actorUserID, routeUserID, name, ag)
	}
	if isClaudeAgent(name, ag.Info()) {
		return h.resetDefaultClaudeSession(ctx, actorUserID, routeUserID, name, ag)
	}
	sessionID, err := ag.ResetSession(ctx, routeUserID)
	if err != nil {
		log.Printf("[handler] reset session failed for %s: %v", routeUserID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	if sessionID != "" {
		return wechatCommandText(fmt.Sprintf("已创建新的%s会话", name), sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

// resetDefaultCodexSessionForRoute 按 route 当前工作空间创建新的 Codex thread。
func (h *Handler) resetDefaultCodexSessionForRoute(ctx context.Context, actorUserID string, routeUserID string, name string, ag agent.Agent) string {
	workspaceRoot := h.codexWorkspaceRootForRoute(actorUserID, routeUserID, name, ag)
	conversationID := buildCodexConversationID(routeUserID, name, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	sessionID, err := ag.ResetSession(ctx, conversationID)
	if err != nil {
		log.Printf("[handler] reset codex session failed for %s: %v", conversationID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	h.recordResetCodexThread(routeUserID, name, workspaceRoot, sessionID)
	if sessionID != "" {
		return wechatCommandText(fmt.Sprintf("已创建新的%s会话", name), sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

// recordResetCodexThread 同步 /new 后的新 thread，避免下一条消息恢复旧工作空间 thread。
func (h *Handler) recordResetCodexThread(userID string, agentName string, workspaceRoot string, threadID string) {
	bindingKey := codexBindingKey(userID, agentName)
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	if strings.TrimSpace(threadID) == "" {
		h.ensureCodexSessions().setPendingNew(bindingKey, workspaceRoot)
		return
	}
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
}

func (h *Handler) resetDefaultClaudeSession(ctx context.Context, actorUserID string, userID string, name string, ag agent.Agent) string {
	workspaceRoot := h.claudeWorkspaceRootForUser(userID, name, ag)
	route := claudeSessionRoute{
		Context: ctx, ActorUserID: actorUserID, UserID: userID, AgentName: name,
		Agent: ag, WorkspaceRoot: workspaceRoot, BindingKey: claudeBindingKey(userID, name),
		Admin: h.isAdminUser(actorUserID),
	}
	return h.handleClaudeNew(route)
}
