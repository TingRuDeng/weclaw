package messaging

import (
	"errors"
	"fmt"
	"log"
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
		log.Printf("[claude-session-acquire] 查找待切换会话失败: %v", err)
		return "查找 Claude 会话失败，请确认 sessionId 或稍后重试。"
	}
	result, err := h.acquireClaudeSessionWithBindingLocked(claudeSessionAcquireRequest{
		Route: route, Selected: selected, Command: "switch",
	})
	if err != nil {
		log.Printf("[claude-session-acquire] 切换绑定失败: %v", err)
		return renderClaudeSessionAcquireFailure(err)
	}
	return h.renderClaudeSelection(route, selected, result)
}

func (h *Handler) renderClaudeSelection(route claudeSessionRoute, selected agent.ClaudeSession, result claudeSessionAcquireResult) string {
	lines := []string{
		"已切换 Claude 会话。",
		"工作空间: " + shortCodexWorkspaceName(selected.Cwd),
		"session: " + selected.ID,
		"运行通道: " + renderClaudeAcquireRuntimeStatus(result.RuntimeErr),
	}
	if result.RuntimeErr != nil {
		log.Printf("[claude-session-acquire] 绑定已提交但运行通道不可用 session=%q: %v", selected.ID, result.RuntimeErr)
		lines = append(lines, "绑定已保留，普通消息暂不会写入；请稍后重试或发送 /cc status 查看状态。")
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
		log.Printf("[claude-workspace] 查找工作空间失败: %v", err)
		return textNavigationResult("查找 Claude 工作空间失败，请发送 /cc ls 查看可选工作空间后重试。")
	}
	if claudeWorkspaceGroupHasPendingCatalog(group) {
		return textNavigationResult(wechatCommandText(
			"当前新会话已创建并绑定。",
			"发送第一条消息后会进入 Claude 会话目录，再发送 /cc ls 即可浏览。",
		))
	}
	workspaceRoot := normalizeClaudeWorkspaceRoot(group.Root)
	if _, err := h.releaseClaudeSelectionForWorkspaceWithBindingLocked(route, workspaceRoot, "cd"); err != nil {
		log.Printf("[claude-workspace] 切换前更新绑定失败: %v", err)
		return textNavigationResult("切换 Claude 工作空间失败，请稍后重试。")
	}
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, workspaceRoot)
	h.bindConversationCwd(route.Agent, conversationID, workspaceRoot)
	sessions, err := h.claudeSessionsForWorkspace(route, workspaceRoot)
	if err != nil {
		log.Printf("[claude-workspace] 查询工作空间会话失败: %v", err)
		return textNavigationResult("查询 Claude 会话失败，请稍后重试。")
	}
	return cardNavigationResult(renderClaudeSessionList(workspaceRoot, sessions))
}

func (h *Handler) handleClaudeNew(route claudeSessionRoute) string {
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(route.BindingKey))
	defer unlock()
	if reply := h.rejectActiveClaudeBindingChange(route); reply != "" {
		return reply
	}
	result, err := h.createAndAcquireClaudeSessionWithBindingLocked(route)
	if err != nil {
		log.Printf("[claude-session-acquire] 新建并绑定失败: %v", err)
		return "新建 Claude 会话失败，请稍后重试。"
	}
	lines := []string{
		"已创建并绑定 Claude 会话。",
		"工作空间: " + shortCodexWorkspaceName(route.WorkspaceRoot),
		"运行通道: " + renderClaudeAcquireRuntimeStatus(result.RuntimeErr),
	}
	if result.RuntimeErr != nil {
		log.Printf("[claude-session-acquire] 新会话绑定已提交但运行通道不可用 session=%q: %v", result.SessionID, result.RuntimeErr)
		lines = append(lines, "绑定已保留，普通消息暂不会写入；请稍后重试或发送 /cc status 查看状态。")
	}
	return wechatCommandText(lines...)
}

func renderClaudeAcquireRuntimeStatus(err error) string {
	if err != nil {
		return "暂不可用（绑定已保留）"
	}
	return "已就绪"
}

// createAndAcquireClaudeSessionWithBindingLocked 先创建真实 session，再提交 frontend binding；
// 只有绑定硬提交失败时才恢复创建前的 runtime。
func (h *Handler) createAndAcquireClaudeSessionWithBindingLocked(route claudeSessionRoute) (claudeSessionAcquireResult, error) {
	workspaceRoot := normalizeClaudeWorkspaceRoot(route.WorkspaceRoot)
	if strings.TrimSpace(route.BindingKey) == "" || workspaceRoot == "" {
		return claudeSessionAcquireResult{}, fmt.Errorf("Claude 新建会话缺少必要字段")
	}
	claudeAgent, ok := route.Agent.(agent.ClaudeSessionAgent)
	if !ok {
		return claudeSessionAcquireResult{}, fmt.Errorf("当前 Claude Agent 不支持 session 切换")
	}
	route.WorkspaceRoot = workspaceRoot
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, workspaceRoot)
	runtimeBefore := captureClaudeRuntimeSelection(claudeAgent, conversationID)
	h.bindConversationCwd(route.Agent, conversationID, workspaceRoot)

	sessionID, createErr := route.Agent.ResetSession(route.Context, conversationID)
	sessionID = strings.TrimSpace(sessionID)
	if createErr != nil || sessionID == "" {
		if createErr == nil {
			createErr = fmt.Errorf("session/new 未返回 sessionId")
		}
		rollbackErr := h.rollbackClaudeSessionAcquire(route, claudeAgent, runtimeBefore)
		if rollbackErr != nil {
			failClosedErr := forceClaudeBindingFailClosedInMemory(h.ensureClaudeSessions(), route.BindingKey)
			return claudeSessionAcquireResult{}, errors.Join(errClaudeSessionAcquireUncertain, createErr, rollbackErr, failClosedErr)
		}
		return claudeSessionAcquireResult{}, errors.Join(createErr, rollbackErr)
	}

	result, acquireErr := h.acquireClaudeSessionWithBindingLocked(claudeSessionAcquireRequest{
		Route: route, Selected: agent.ClaudeSession{ID: sessionID, Cwd: workspaceRoot}, Command: "new",
	})
	if acquireErr == nil {
		return result, nil
	}
	rollbackErr := h.rollbackClaudeSessionAcquire(route, claudeAgent, runtimeBefore)
	if rollbackErr != nil {
		failClosedErr := forceClaudeBindingFailClosedInMemory(h.ensureClaudeSessions(), route.BindingKey)
		return claudeSessionAcquireResult{}, errors.Join(errClaudeSessionAcquireUncertain, acquireErr, rollbackErr, failClosedErr)
	}
	return claudeSessionAcquireResult{}, acquireErr
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
