package messaging

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

func (h *Handler) claudeWorkspaceRoot(agentName string) string {
	h.mu.RLock()
	workspaceRoot := h.agentWorkDirs[agentName]
	h.mu.RUnlock()
	if workspaceRoot == "" {
		workspaceRoot = defaultAttachmentWorkspace()
	}
	return normalizeClaudeWorkspaceRoot(workspaceRoot)
}

func (h *Handler) claudeWorkspaceRootForUser(userID string, agentName string, _ agent.Agent) string {
	binding := h.ensureClaudeSessions().binding(claudeBindingKey(userID, agentName))
	if binding.WorkspaceRoot != "" {
		return binding.WorkspaceRoot
	}
	return h.claudeWorkspaceRoot(agentName)
}

func (h *Handler) handleClaudeSwitch(route claudeSessionRoute, target string) string {
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(route.BindingKey))
	defer unlock()
	if reply := h.rejectActiveClaudeBindingChange(route); reply != "" {
		return reply
	}
	selected, err := h.findClaudeSessionForRoute(route, target)
	if err != nil {
		return err.Error()
	}
	if err := h.commitClaudeSelection(route, selected); err != nil {
		return fmt.Sprintf("切换 Claude 会话失败: %v", err)
	}
	return h.renderClaudeSelection(route, selected)
}

func (h *Handler) commitClaudeSelection(route claudeSessionRoute, selected agent.ClaudeSession) error {
	claudeAgent, ok := route.Agent.(agent.ClaudeSessionAgent)
	if !ok {
		return fmt.Errorf("当前 Claude Agent 不支持 session 切换")
	}
	store := h.ensureClaudeSessions()
	previous, existed := store.bindingSnapshot(route.BindingKey)
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, selected.Cwd)
	h.bindConversationCwd(route.Agent, conversationID, selected.Cwd)
	if err := claudeAgent.UseClaudeSession(route.Context, conversationID, selected.ID); err != nil {
		return err
	}
	if err := store.commitSelection(route.BindingKey, selected.Cwd, selected.ID); err != nil {
		return errors.Join(err, h.rollbackClaudeRuntime(route, conversationID, previous))
	}
	if err := h.ensureAgentSessions().Set(route.UserID, route.AgentName); err != nil {
		storeErr := store.restoreBinding(route.BindingKey, previous, existed)
		runtimeErr := h.rollbackClaudeRuntime(route, conversationID, previous)
		return errors.Join(err, storeErr, runtimeErr)
	}
	return nil
}

func (h *Handler) rollbackClaudeRuntime(route claudeSessionRoute, currentConversationID string, previous claudeSessionBinding) error {
	claudeAgent, ok := route.Agent.(agent.ClaudeSessionAgent)
	if !ok {
		return nil
	}
	claudeAgent.ClearClaudeSession(currentConversationID)
	if previous.SessionID == "" {
		return nil
	}
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, previous.WorkspaceRoot)
	h.bindConversationCwd(route.Agent, conversationID, previous.WorkspaceRoot)
	return claudeAgent.UseClaudeSession(route.Context, conversationID, previous.SessionID)
}

func (h *Handler) renderClaudeSelection(route claudeSessionRoute, selected agent.ClaudeSession) string {
	lines := []string{
		"已切换 Claude 会话。",
		"工作空间: " + shortCodexWorkspaceName(selected.Cwd),
		"session: " + selected.ID,
		"恢复状态: 已就绪",
	}
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, selected.Cwd)
	if configAgent, ok := route.Agent.(agent.ClaudeSessionConfigAgent); ok {
		if config, found := configAgent.ClaudeSessionConfig(conversationID); found {
			lines = append(lines, renderSessionModelStatus(sessionModelStatus{Model: config.Model, Effort: config.Effort})...)
			return wechatCommandText(lines...)
		}
	}
	lines = append(lines, renderSessionModelStatus(sessionModelStatus{Model: selected.Config.Model, Effort: selected.Config.Effort})...)
	return wechatCommandText(lines...)
}

// handleClaudeCdResult 返回工作空间导航文本及卡片展示状态。
func (h *Handler) handleClaudeCdResult(route claudeSessionRoute, target string) navigationCommandResult {
	if strings.TrimSpace(target) == ".." {
		return cardNavigationResult(h.renderClaudeWorkspaceGroups(route))
	}
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(route.BindingKey))
	defer unlock()
	if reply := h.rejectActiveClaudeBindingChange(route); reply != "" {
		return textNavigationResult(reply)
	}
	group, err := h.findClaudeWorkspaceGroupForRoute(route, target)
	if err != nil {
		return textNavigationResult(err.Error())
	}
	workspaceRoot := normalizeClaudeWorkspaceRoot(group.Root)
	if err := h.ensureClaudeSessions().commitWorkspace(route.BindingKey, workspaceRoot); err != nil {
		return textNavigationResult(fmt.Sprintf("切换 Claude 工作空间失败: %v", err))
	}
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, workspaceRoot)
	h.bindConversationCwd(route.Agent, conversationID, workspaceRoot)
	sessions, err := h.claudeSessionsForWorkspace(route, workspaceRoot)
	if err != nil {
		return textNavigationResult(err.Error())
	}
	return cardNavigationResult(renderClaudeSessionList(workspaceRoot, sessions))
}

func (h *Handler) handleClaudeNew(route claudeSessionRoute) string {
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(route.BindingKey))
	defer unlock()
	if reply := h.rejectActiveClaudeBindingChange(route); reply != "" {
		return reply
	}
	previous := h.ensureClaudeSessions().binding(route.BindingKey)
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, route.WorkspaceRoot)
	h.bindConversationCwd(route.Agent, conversationID, route.WorkspaceRoot)
	sessionID, err := route.Agent.ResetSession(route.Context, conversationID)
	if err != nil || strings.TrimSpace(sessionID) == "" {
		createErr := firstError(err, fmt.Errorf("session/new 未返回 sessionId"))
		rollbackErr := h.rollbackClaudeRuntime(route, conversationID, previous)
		return fmt.Sprintf("新建 Claude 会话失败: %v", errors.Join(createErr, rollbackErr))
	}
	selected := agent.ClaudeSession{ID: sessionID, Cwd: route.WorkspaceRoot}
	if err := h.commitNewClaudeSelection(route, conversationID, selected); err != nil {
		return fmt.Sprintf("新建 Claude 会话失败: %v", err)
	}
	return wechatCommandText("已创建新的 Claude 会话。", "工作空间: "+shortCodexWorkspaceName(route.WorkspaceRoot))
}

// rejectActiveClaudeBindingChange 防止活动任务因 workspace/session 漂移而失去控制键。
func (h *Handler) rejectActiveClaudeBindingChange(route claudeSessionRoute) string {
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	workspaceRoot := binding.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = route.WorkspaceRoot
	}
	if !h.hasActiveClaudeTask(route, workspaceRoot) {
		return ""
	}
	return "当前 Claude 任务正在运行，请等待任务结束或先发送 /stop。"
}

func (h *Handler) commitNewClaudeSelection(route claudeSessionRoute, conversationID string, selected agent.ClaudeSession) error {
	store := h.ensureClaudeSessions()
	previous, existed := store.bindingSnapshot(route.BindingKey)
	if err := store.commitSelection(route.BindingKey, selected.Cwd, selected.ID); err != nil {
		return errors.Join(err, h.rollbackClaudeRuntime(route, conversationID, previous))
	}
	if err := h.ensureAgentSessions().Set(route.UserID, route.AgentName); err != nil {
		return errors.Join(err, store.restoreBinding(route.BindingKey, previous, existed), h.rollbackClaudeRuntime(route, conversationID, previous))
	}
	return nil
}

func firstError(primary error, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
