package messaging

import (
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

func (h *Handler) claudeWorkspaceRootForUser(userID string, agentName string, ag agent.Agent) string {
	bindingKey := claudeBindingKey(userID, agentName)
	workspaceRoot, ok := h.ensureClaudeSessions().getActiveWorkspace(bindingKey)
	if !ok {
		return h.claudeWorkspaceRoot(agentName)
	}
	ag.SetCwd(workspaceRoot)
	h.mu.Lock()
	if h.agentWorkDirs == nil {
		h.agentWorkDirs = make(map[string]string)
	}
	h.agentWorkDirs[agentName] = workspaceRoot
	h.mu.Unlock()
	return workspaceRoot
}

func (h *Handler) handleClaudeSwitch(route claudeSessionRoute, target string) string {
	claudeAg, ok := route.Agent.(agent.ClaudeSessionAgent)
	if !ok {
		return "当前 Claude Agent 不支持 session 切换。"
	}
	workspaceRoot, sessionID, err := h.resolveClaudeSwitchTarget(route, target)
	if err != nil {
		return err.Error()
	}
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, workspaceRoot)
	if err := claudeAg.UseClaudeSession(route.Context, conversationID, sessionID); err != nil {
		return fmt.Sprintf("切换 Claude 会话失败: %v", err)
	}
	h.ensureClaudeSessions().setSession(route.BindingKey, workspaceRoot, sessionID)
	h.ensureClaudeSessions().setActiveWorkspace(route.BindingKey, workspaceRoot)
	return wechatCommandText("已切换 Claude 会话。", "工作空间: "+shortCodexWorkspaceName(workspaceRoot))
}

func (h *Handler) resolveClaudeSwitchTarget(route claudeSessionRoute, target string) (string, string, error) {
	target = strings.TrimSpace(target)
	if index, ok := parseCodexListIndex(target); ok {
		return h.resolveClaudeSwitchIndex(route, index)
	}
	sessionID := target
	workspaceRoot := h.resolveClaudeSwitchWorkspace(route, sessionID)
	return workspaceRoot, sessionID, nil
}

func (h *Handler) resolveClaudeSwitchIndex(route claudeSessionRoute, index int) (string, string, error) {
	views := h.claudeSwitchTargets(route.BindingKey)
	if index < 0 || index >= len(views) {
		return "", "", fmt.Errorf("编号不存在，请先发送 /cc ls 查看可切换会话。")
	}
	view := views[index]
	if strings.TrimSpace(view.ThreadID) == "" || view.PendingNewThread {
		return "", "", fmt.Errorf("该编号当前没有可切换的会话。")
	}
	return h.switchClaudeWorkspace(route.AgentName, view.WorkspaceRoot, route.Agent), view.ThreadID, nil
}

func (h *Handler) resolveClaudeSwitchWorkspace(route claudeSessionRoute, sessionID string) string {
	if workspaceRoot, ok := h.ensureClaudeSessions().findWorkspaceBySession(route.BindingKey, sessionID); ok {
		return h.switchClaudeWorkspace(route.AgentName, workspaceRoot, route.Agent)
	}
	if workspaceRoot, ok := h.findLocalClaudeWorkspaceBySession(sessionID); ok {
		return h.switchClaudeWorkspace(route.AgentName, workspaceRoot, route.Agent)
	}
	return route.WorkspaceRoot
}

func (h *Handler) switchClaudeWorkspace(agentName string, workspaceRoot string, ag agent.Agent) string {
	workspaceRoot = normalizeClaudeWorkspaceRoot(workspaceRoot)
	ag.SetCwd(workspaceRoot)
	h.mu.Lock()
	if h.agentWorkDirs == nil {
		h.agentWorkDirs = make(map[string]string)
	}
	h.agentWorkDirs[agentName] = workspaceRoot
	h.mu.Unlock()
	return workspaceRoot
}

func (h *Handler) handleClaudeNew(route claudeSessionRoute) string {
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, route.WorkspaceRoot)
	if claudeAg, ok := route.Agent.(agent.ClaudeSessionAgent); ok {
		claudeAg.ClearClaudeSession(conversationID)
	}
	h.ensureClaudeSessions().setPendingNew(route.BindingKey, route.WorkspaceRoot)
	h.ensureClaudeSessions().setActiveWorkspace(route.BindingKey, route.WorkspaceRoot)
	return wechatCommandText("已切换到新的 Claude 会话。", "workspace: "+route.WorkspaceRoot)
}

func (h *Handler) syncClaudeSessionFromAgent(route claudeSessionRoute) {
	claudeAg, ok := route.Agent.(agent.ClaudeSessionAgent)
	if !ok {
		return
	}
	if _, pending := h.ensureClaudeSessions().getSession(route.BindingKey, route.WorkspaceRoot); pending {
		return
	}
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, route.WorkspaceRoot)
	sessionID, ok := claudeAg.CurrentClaudeSession(conversationID)
	if ok {
		h.ensureClaudeSessions().setSession(route.BindingKey, route.WorkspaceRoot, sessionID)
	}
}
