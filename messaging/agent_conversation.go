package messaging

import (
	"context"
	"errors"
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

func (h *Handler) codexConversationRouteForSession(ownerUserID string, routeUserID string, agentName string, ag agent.Agent) codexConversationRoute {
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = ownerUserID
	}
	workspaceRoot := h.codexWorkspaceRootForRoute(ownerUserID, routeUserID, agentName, ag)
	conversationID := buildCodexConversationID(routeUserID, agentName, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	threadID, pending := h.ensureCodexSessions().getThread(codexBindingKey(routeUserID, agentName), workspaceRoot)
	if pending {
		threadID = ""
	}
	return codexConversationRoute{
		bindingKey:     codexBindingKey(routeUserID, agentName),
		workspaceRoot:  workspaceRoot,
		conversationID: conversationID,
		threadID:       threadID,
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
		return fmt.Errorf("当前窗口没有有效的 Codex 会话，请发送 /cx ls 选择或 /cx new 新建")
	}
	if route.threadID != "" {
		threadID = route.threadID
	}
	if threadID == "" {
		return fmt.Errorf("当前窗口没有有效的 Codex 会话，请发送 /cx ls 选择或 /cx new 新建")
	}
	resolveOpts := codexRuntimeResolveOptions{route: route, threadID: threadID, ag: ag}
	var resolution codexRuntimeResolution
	var err error
	if _, live := ag.(agent.CodexLiveRuntimeAgent); live {
		resolution, err = h.resolveBoundCodexRuntimeLocked(resolveOpts)
		if err == nil {
			err = ensureCodexRouteOwnsControl(resolution.Request.Intent, route)
		}
	} else {
		resolution, err = h.resolveCodexRuntime(ctx, resolveOpts)
	}
	if err != nil {
		return fmt.Errorf("恢复 Codex 会话失败: %w", err)
	}
	if err := ensureCodexRuntimeReady(resolution, route); err != nil {
		return fmt.Errorf("恢复 Codex 会话失败: %w", err)
	}
	h.ensureCodexSessions().ensureWorkspace(route.bindingKey, route.workspaceRoot)
	return nil
}

func (h *Handler) resolveClaudeConversationIDForRoute(ctx context.Context, ownerUserID string, routeUserID string, agentName string, ag agent.Agent) (string, error) {
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = ownerUserID
	}
	workspaceRoot := h.claudeWorkspaceRootForUser(routeUserID, agentName, ag)
	if !h.workspaceAllowedForAgentContext(ctx, agentName, workspaceRoot) {
		return "", fmt.Errorf("当前工作空间不在允许范围，请发送 /cc ls 重新选择")
	}
	bindingKey := claudeBindingKey(routeUserID, agentName)
	conversationID := buildClaudeConversationID(routeUserID, agentName, workspaceRoot)
	claudeAg, ok := ag.(agent.ClaudeSessionAgent)
	if !ok || !strings.EqualFold(ag.Info().Type, "acp") {
		h.bindConversationCwd(ag, conversationID, workspaceRoot)
		return conversationID, nil
	}
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(bindingKey))
	defer unlock()
	binding, _, controlErr := h.ensureClaudeSessions().requireRemoteControl(bindingKey)
	if controlErr != nil {
		return "", errors.New(renderClaudeRemoteControlError(controlErr))
	}
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	if binding.SessionID == "" || binding.Status == claudeBindingUnbound {
		return "", fmt.Errorf("当前窗口没有有效的 Claude 会话，请发送 /cc ls 选择或 /cc new 新建")
	}
	if binding.Status == claudeBindingResumeFailed {
		return "", fmt.Errorf("原 Claude 会话恢复失败，请发送 /cc ls 重新选择或 /cc new 新建")
	}
	if binding.Status == claudeBindingPendingResume {
		return h.resumePendingClaudeBinding(ctx, claudeResumeRequest{
			bindingKey: bindingKey, conversationID: conversationID, sessionID: binding.SessionID, agent: claudeAg,
		})
	}
	return conversationID, nil
}

type claudeResumeRequest struct {
	bindingKey     string
	conversationID string
	sessionID      string
	agent          agent.ClaudeSessionAgent
}

func (h *Handler) resumePendingClaudeBinding(ctx context.Context, req claudeResumeRequest) (string, error) {
	if err := req.agent.UseClaudeSession(ctx, req.conversationID, req.sessionID); err != nil {
		markErr := h.ensureClaudeSessions().markResumeFailed(req.bindingKey)
		return "", fmt.Errorf("恢复 Claude 会话失败: %w；请发送 /cc ls 重新选择或 /cc new 新建", errors.Join(err, markErr))
	}
	if err := h.ensureClaudeSessions().markReady(req.bindingKey); err != nil {
		return "", fmt.Errorf("保存 Claude 恢复状态失败: %w", err)
	}
	return req.conversationID, nil
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

func (h *Handler) syncCodexThreadFromAgent(userID string, agentName string, workspaceRoot string, ag agent.Agent) {
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		return
	}
	bindingKey := codexBindingKey(userID, agentName)
	store := h.ensureCodexSessions()
	threadID, pending := store.getThread(bindingKey, workspaceRoot)
	if pending || strings.TrimSpace(threadID) != "" {
		return
	}
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
	threadID, ok = codexAg.CurrentCodexThread(conversationID)
	if ok {
		store.setThread(bindingKey, workspaceRoot, threadID)
	}
}
