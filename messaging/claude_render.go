package messaging

import (
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

func (h *Handler) renderClaudeWhoami(route claudeSessionRoute) string {
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	return wechatCommandText(
		"workspace: "+route.WorkspaceRoot,
		"session: "+renderClaudeBindingSession(binding),
	)
}

func (h *Handler) renderClaudeStatus(route claudeSessionRoute) string {
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	lines := []string{
		"Claude 状态:",
		"工作空间: " + route.WorkspaceRoot,
		"session: " + renderClaudeBindingSession(binding),
		"恢复状态: " + renderClaudeBindingStatus(binding.Status),
		"运行模式: 单一共享 ClaudeHost (" + route.Agent.Info().Type + ")",
		"writer: " + h.renderClaudeWriterStatus(route, binding),
		"写入规则: 多窗口可绑定，同一 session 单 writer",
	}
	if hostAgent, ok := route.Agent.(agent.ClaudeHostRuntimeAgent); ok {
		host := hostAgent.ClaudeHostStatus()
		hostState := "未连接"
		if host.Started {
			hostState = "已连接"
		}
		lines = append(lines, fmt.Sprintf("ClaudeHost: %s (pid=%d, generation=%d)", hostState, host.PID, host.Generation))
	}
	return wechatCommandText(append(lines, h.claudeConfigStatus(route)...)...)
}

func (h *Handler) renderClaudeWriterStatus(route claudeSessionRoute, binding claudeSessionBinding) string {
	sessionID := strings.TrimSpace(binding.SessionID)
	if sessionID == "" {
		return "空闲（未绑定）"
	}
	task, active := h.activeTask(claudeSessionExecutionKey(sessionID))
	if !active {
		return "空闲"
	}
	task.mu.Lock()
	writerRoute := task.routeUserID
	hasPending := strings.TrimSpace(task.pending.message) != ""
	task.mu.Unlock()
	state := "其他窗口执行中"
	if writerRoute == strings.TrimSpace(route.UserID) {
		state = "当前窗口执行中"
	}
	if hasPending {
		state += "（已有暂存消息）"
	}
	return state
}

func renderClaudeBindingStatus(status claudeBindingStatus) string {
	switch status {
	case claudeBindingPendingResume:
		return "等待恢复"
	case claudeBindingReady:
		return "已就绪"
	case claudeBindingResumeFailed:
		return "运行通道暂不可用（绑定已保留）"
	default:
		return "未绑定"
	}
}

func (h *Handler) claudeConfigStatus(route claudeSessionRoute) []string {
	configAgent, ok := route.Agent.(agent.ClaudeSessionConfigAgent)
	if !ok {
		return nil
	}
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, route.WorkspaceRoot)
	config, found := configAgent.ClaudeSessionConfig(conversationID)
	if !found {
		return nil
	}
	return renderSessionModelStatus(sessionModelStatus{Model: config.Model, Effort: config.Effort})
}

func renderClaudeBindingSession(binding claudeSessionBinding) string {
	if strings.TrimSpace(binding.SessionID) == "" {
		return "未绑定"
	}
	return binding.SessionID
}

func (h *Handler) renderClaudeWorkspaceList(route claudeSessionRoute) string {
	views, err := h.claudeDisplayTargets(route)
	if err != nil {
		log.Printf("[claude-session] 查询会话列表失败: %v", err)
		return "查询 Claude 会话失败，请稍后重试。"
	}
	if len(views) == 0 {
		return "当前还没有可切换的 Claude 会话。"
	}
	lines := []string{"Claude 会话:"}
	index := 0
	for _, view := range views {
		if view.PendingCatalog {
			lines = append(lines, "当前新会话: "+shortCodexWorkspaceName(view.WorkspaceRoot)+"（发送第一条消息后进入历史目录）")
			continue
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index, claudeSessionListLabel(view)))
		index++
	}
	return wechatCommandText(lines...)
}

// renderClaudeWorkspaceGroups 渲染 `/cc cd ..` 使用的工作空间文本列表。
func (h *Handler) renderClaudeWorkspaceGroups(route claudeSessionRoute) string {
	groups, err := h.claudeWorkspaceGroupsForRoute(route)
	if err != nil {
		log.Printf("[claude-workspace] 查询工作空间列表失败: %v", err)
		return "查询 Claude 工作空间失败，请稍后重试。"
	}
	if len(groups) == 0 {
		return "当前还没有 Claude 工作空间。"
	}
	lines := []string{"Claude 工作空间:"}
	for index, group := range groups {
		lines = append(lines, fmt.Sprintf("%d. %s", index, claudeWorkspaceGroupLabel(group)))
	}
	return wechatCommandText(lines...)
}

// renderClaudeSessionList 渲染工作空间内会话，并明确要求空空间使用 `/cc new`。
func renderClaudeSessionList(workspaceRoot string, sessions []codexWorkspaceView) string {
	workspaceName := shortCodexWorkspaceName(workspaceRoot)
	if len(sessions) == 0 {
		return wechatCommandText("工作空间: "+workspaceName, "当前工作空间还没有可切换会话，请发送 /cc new。")
	}
	lines := []string{workspaceName + " 会话:"}
	for index, session := range sessions {
		lines = append(lines, fmt.Sprintf("%d. %s", index, codexSessionDisplayName(session)))
	}
	lines = append(lines, "", "发送 /cc cd .. 返回工作空间列表。")
	return wechatCommandText(lines...)
}

func claudeSessionListLabel(view codexWorkspaceView) string {
	workspaceName := shortCodexWorkspaceName(view.WorkspaceRoot)
	sessionName := codexSessionDisplayName(view)
	if workspaceName == "" {
		return sessionName
	}
	return workspaceName + " / " + sessionName
}

func buildClaudeSessionHelpText() string {
	return wechatCommandText(
		"Claude 会话命令:",
		"/cc whoami 查看当前 workspace/session 绑定",
		"/cc ls 查看可切换会话",
		"/cc cd <编号|..> 进入工作空间或返回列表",
		"/cc switch <编号|sessionId> 切换 Claude 会话",
		"/cc new 新建当前工作空间会话",
		"/cc pwd 查看当前工作空间",
		"/cc status 查看 binding、共享 ClaudeHost 和 writer 状态",
		"/cc quota 查看 Claude 账号额度",
		"/cc model status 查看新建 Claude 会话的默认模型配置",
		"/cc model ls 查看 Claude 可选模型",
	)
}

func isClaudeAgent(name string, info agent.AgentInfo) bool {
	if strings.EqualFold(strings.TrimSpace(name), "claude") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(info.Name)) {
	case "claude", "claude-agent-acp", "@agentclientprotocol/claude-agent-acp":
		return true
	default:
		return false
	}
}
