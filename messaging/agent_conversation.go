package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
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
		bindingKey := claudeBindingKey(routeUserID, agentName)
		if binding, ok := h.ensureClaudeSessions().bindingSnapshot(bindingKey); ok && strings.TrimSpace(binding.SessionID) != "" {
			return claudeSessionExecutionKey(binding.SessionID)
		}
		workspaceRoot := h.claudeWorkspaceRootForUser(routeUserID, agentName, ag)
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
	if _, live := ag.(agent.CodexLiveRuntimeAgent); !live {
		resolution, err = h.resolveCodexRuntime(ctx, resolveOpts)
	}
	if err != nil {
		return fmt.Errorf("恢复 Codex 会话失败: %w", err)
	}
	if _, live := ag.(agent.CodexLiveRuntimeAgent); !live {
		if err := ensureCodexRuntimeReady(resolution, route); err != nil {
			return fmt.Errorf("恢复 Codex 会话失败: %w", err)
		}
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
	binding, bindingErr := h.ensureClaudeSessions().requireWritableBinding(bindingKey)
	if bindingErr != nil {
		return "", errors.New(renderClaudeBindingError(bindingErr))
	}
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	if binding.SessionID == "" || binding.Status == claudeBindingUnbound {
		return "", fmt.Errorf("当前窗口没有有效的 Claude 会话，请发送 /cc ls 选择或 /cc new 新建")
	}
	if binding.Status == claudeBindingPendingResume {
		return h.resumePendingClaudeBinding(ctx, claudeResumeRequest{
			bindingKey: bindingKey, conversationID: conversationID, sessionID: binding.SessionID,
			bindingRevision: binding.Revision, agent: claudeAg,
		})
	}
	return conversationID, nil
}

type claudeResumeRequest struct {
	bindingKey      string
	conversationID  string
	sessionID       string
	bindingRevision uint64
	agent           agent.ClaudeSessionAgent
}

func (h *Handler) resumePendingClaudeBinding(ctx context.Context, req claudeResumeRequest) (string, error) {
	unlockSession, lockErr := h.lockClaudeSessionControls(claudeSessionLockRequest{
		ctx: ctx, command: "pending resume", sessionIDs: []string{req.sessionID},
	})
	if lockErr != nil {
		return "", fmt.Errorf("等待共享 ClaudeHost session 恢复: %w", lockErr)
	}
	defer unlockSession()
	if err := h.ensureClaudeSessions().validateBindingSnapshot(req.bindingKey, claudeTaskBindingSnapshot{
		SessionID: req.sessionID, Revision: req.bindingRevision,
	}); err != nil {
		return "", errors.New(renderClaudeBindingError(err))
	}
	if err := req.agent.UseClaudeSession(ctx, req.conversationID, req.sessionID); err != nil {
		markErr := h.ensureClaudeSessions().markResumeFailed(req.bindingKey)
		log.Printf("[claude-runtime] 恢复绑定失败 session=%q: %v", req.sessionID, errors.Join(err, markErr))
		return "", errors.New(renderClaudeBindingError(errClaudeRuntimeUnavailable))
	}
	if err := h.ensureClaudeSessions().markReady(req.bindingKey); err != nil {
		log.Printf("[claude-runtime] 保存恢复状态失败 session=%q: %v", req.sessionID, err)
		_ = h.ensureClaudeSessions().markResumeFailed(req.bindingKey)
		return "", errors.New(renderClaudeBindingError(errClaudeRuntimeUnavailable))
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
	bindingKey := codexBindingKey(userID, agentName)
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	store := h.ensureCodexSessions()
	if _, live := ag.(agent.CodexLiveRuntimeAgent); live {
		// Live turn 已由 route 绑定到显式选择；ACP 的兼容映射可能仍指向旧 Desktop thread。
		if selected, pending := store.getThread(bindingKey, workspaceRoot); !pending && strings.TrimSpace(selected) != "" {
			store.clearPendingFirstTurn(bindingKey, workspaceRoot, selected)
			return workspaceRoot, true
		}
	}
	threadID, ok := codexAg.CurrentCodexThread(conversationID)
	if !ok {
		return "", false
	}
	if ownerWorkspace, ok := store.findWorkspaceByThread(bindingKey, threadID); ok {
		workspaceRoot = ownerWorkspace
	}
	store.setThread(bindingKey, workspaceRoot, threadID)
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
