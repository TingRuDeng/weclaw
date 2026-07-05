package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

func (h *Handler) agentExecutionKey(userID string, agentName string, ag agent.Agent) string {
	return h.agentExecutionKeyForRoute(userID, userID, agentName, ag)
}

func (h *Handler) agentExecutionKeyForRoute(ownerUserID string, routeUserID string, agentName string, ag agent.Agent) string {
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = ownerUserID
	}
	info := ag.Info()
	if isCodexAgent(agentName, info) {
		return h.codexConversationRouteForSession(ownerUserID, routeUserID, agentName, ag).conversationID
	}
	if isClaudeAgent(agentName, info) {
		workspaceRoot := h.claudeWorkspaceRootForUser(ownerUserID, agentName, ag)
		return buildClaudeConversationID(routeUserID, agentName, workspaceRoot)
	}
	return strings.Join([]string{"agent", strings.TrimSpace(routeUserID), strings.TrimSpace(agentName)}, "\x00")
}

func (h *Handler) codexConversationRouteForUser(userID string, agentName string, ag agent.Agent) codexConversationRoute {
	return h.codexConversationRouteForSession(userID, userID, agentName, ag)
}

func (h *Handler) codexConversationRouteForSession(ownerUserID string, routeUserID string, agentName string, ag agent.Agent) codexConversationRoute {
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = ownerUserID
	}
	workspaceRoot := h.codexWorkspaceRootForUser(ownerUserID, agentName, ag)
	conversationID := buildCodexConversationID(routeUserID, agentName, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	return codexConversationRoute{
		bindingKey:     codexBindingKey(routeUserID, agentName),
		workspaceRoot:  workspaceRoot,
		conversationID: conversationID,
	}
}

func (h *Handler) bindConversationCwd(ag agent.Agent, conversationID string, workspaceRoot string) {
	if workspaceAg, ok := ag.(agent.ConversationWorkspaceAgent); ok {
		workspaceAg.SetConversationCwd(conversationID, workspaceRoot)
	}
}

func (h *Handler) allowedAttachmentRoots(agentName string) []string {
	roots := []string{defaultAttachmentWorkspace()}

	h.mu.RLock()
	agentDir := h.agentWorkDirs[agentName]
	workspaceRoots := append([]string(nil), h.allowedWorkspaceRoots...)
	h.mu.RUnlock()

	if agentDir != "" {
		roots = append(roots, agentDir)
	}
	// 允许回传 agent 在已授权工作目录(白名单)内生成的产物。
	roots = append(roots, workspaceRoots...)

	return roots
}

func (h *Handler) resolveAgentConversationID(ctx context.Context, userID string, agentName string, ag agent.Agent) (string, error) {
	return h.resolveAgentConversationIDForRoute(ctx, userID, userID, agentName, ag)
}

func (h *Handler) resolveAgentConversationIDForRoute(ctx context.Context, ownerUserID string, routeUserID string, agentName string, ag agent.Agent) (string, error) {
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = ownerUserID
	}
	if isCodexAgent(agentName, ag.Info()) {
		return h.resolveCodexConversationIDForRoute(ctx, ownerUserID, routeUserID, agentName, ag)
	}
	if isClaudeAgent(agentName, ag.Info()) {
		return h.resolveClaudeConversationIDForRoute(ctx, ownerUserID, routeUserID, agentName, ag)
	}
	return routeUserID, nil
}

func (h *Handler) resolveCodexConversationID(ctx context.Context, userID string, agentName string, ag agent.Agent) (string, error) {
	return h.resolveCodexConversationIDForRoute(ctx, userID, userID, agentName, ag)
}

func (h *Handler) resolveCodexConversationIDForRoute(ctx context.Context, ownerUserID string, routeUserID string, agentName string, ag agent.Agent) (string, error) {
	route := h.codexConversationRouteForSession(ownerUserID, routeUserID, agentName, ag)
	if err := h.prepareCodexConversation(ctx, route, ag); err != nil {
		return "", err
	}
	return route.conversationID, nil
}

func (h *Handler) prepareCodexConversation(ctx context.Context, route codexConversationRoute, ag agent.Agent) error {
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		h.ensureCodexSessions().ensureWorkspace(route.bindingKey, route.workspaceRoot)
		return nil
	}
	threadID, pending := h.ensureCodexSessions().getThread(route.bindingKey, route.workspaceRoot)
	if pending {
		codexAg.ClearCodexThread(route.conversationID)
		return nil
	}
	if threadID != "" {
		current, hasCurrent := codexAg.CurrentCodexThread(route.conversationID)
		if !hasCurrent || current != threadID {
			if err := codexAg.UseCodexThread(ctx, route.conversationID, threadID); err != nil {
				return fmt.Errorf("恢复 Codex 会话失败: %w", err)
			}
		}
	}
	h.ensureCodexSessions().ensureWorkspace(route.bindingKey, route.workspaceRoot)
	return nil
}

func (h *Handler) resolveClaudeConversationID(ctx context.Context, userID string, agentName string, ag agent.Agent) (string, error) {
	return h.resolveClaudeConversationIDForRoute(ctx, userID, userID, agentName, ag)
}

func (h *Handler) resolveClaudeConversationIDForRoute(ctx context.Context, ownerUserID string, routeUserID string, agentName string, ag agent.Agent) (string, error) {
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = ownerUserID
	}
	workspaceRoot := h.claudeWorkspaceRootForUser(ownerUserID, agentName, ag)
	bindingKey := claudeBindingKey(routeUserID, agentName)
	conversationID := buildClaudeConversationID(routeUserID, agentName, workspaceRoot)
	claudeAg, ok := ag.(agent.ClaudeSessionAgent)
	if !ok {
		h.ensureClaudeSessions().ensureWorkspace(bindingKey, workspaceRoot)
		return conversationID, nil
	}
	sessionID, pending := h.ensureClaudeSessions().getSession(bindingKey, workspaceRoot)
	if pending {
		claudeAg.ClearClaudeSession(conversationID)
		return conversationID, nil
	}
	if sessionID != "" {
		current, hasCurrent := claudeAg.CurrentClaudeSession(conversationID)
		if !hasCurrent || current != sessionID {
			if err := claudeAg.UseClaudeSession(ctx, conversationID, sessionID); err != nil {
				return "", fmt.Errorf("恢复 Claude 会话失败: %w", err)
			}
		}
	}
	h.ensureClaudeSessions().ensureWorkspace(bindingKey, workspaceRoot)
	return conversationID, nil
}

func (h *Handler) recordCodexThread(userID string, agentName string, ag agent.Agent, conversationID string) {
	workspaceRoot := h.codexWorkspaceRootForUser(userID, agentName, ag)
	if recordedWorkspace, ok := h.recordCodexThreadForWorkspace(userID, agentName, ag, conversationID, workspaceRoot); ok {
		h.ensureCodexSessions().setActiveWorkspace(codexBindingKey(userID, agentName), recordedWorkspace)
	}
}

func (h *Handler) recordCodexThreadForWorkspace(userID string, agentName string, ag agent.Agent, conversationID string, workspaceRoot string) (string, bool) {
	if !isCodexAgent(agentName, ag.Info()) {
		return "", false
	}
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		return "", false
	}
	threadID, ok := codexAg.CurrentCodexThread(conversationID)
	if !ok {
		return "", false
	}
	bindingKey := codexBindingKey(userID, agentName)
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	if ownerWorkspace, ok := h.ensureCodexSessions().findWorkspaceByThread(bindingKey, threadID); ok {
		workspaceRoot = ownerWorkspace
	}
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
	return workspaceRoot, true
}

func (h *Handler) recordClaudeSession(userID string, agentName string, ag agent.Agent, conversationID string) {
	h.recordClaudeSessionForRoute(userID, userID, agentName, ag, conversationID)
}

func (h *Handler) recordClaudeSessionForRoute(ownerUserID string, routeUserID string, agentName string, ag agent.Agent, conversationID string) {
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = ownerUserID
	}
	if !isClaudeAgent(agentName, ag.Info()) {
		return
	}
	claudeAg, ok := ag.(agent.ClaudeSessionAgent)
	if !ok {
		return
	}
	sessionID, ok := claudeAg.CurrentClaudeSession(conversationID)
	if !ok {
		return
	}
	workspaceRoot := h.claudeWorkspaceRootForUser(ownerUserID, agentName, ag)
	bindingKey := claudeBindingKey(routeUserID, agentName)
	if ownerWorkspace, ok := h.ensureClaudeSessions().findWorkspaceBySession(bindingKey, sessionID); ok {
		workspaceRoot = ownerWorkspace
	}
	h.ensureClaudeSessions().setSession(bindingKey, workspaceRoot, sessionID)
	h.ensureClaudeSessions().setActiveWorkspace(bindingKey, workspaceRoot)
}

func (h *Handler) syncCodexThreadFromAgent(userID string, agentName string, workspaceRoot string, ag agent.Agent) {
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		return
	}
	bindingKey := codexBindingKey(userID, agentName)
	if _, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot); pending {
		return
	}
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
	threadID, ok := codexAg.CurrentCodexThread(conversationID)
	if ok {
		h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
	}
}
