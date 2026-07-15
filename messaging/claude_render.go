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
	intent := h.ensureClaudeSessions().controlIntent(binding.SessionID)
	lines := []string{
		"Claude 状态:",
		"工作空间: " + route.WorkspaceRoot,
		"session: " + renderClaudeBindingSession(binding),
		"恢复状态: " + renderClaudeBindingStatus(binding.Status),
		"控制方: " + renderClaudeControlOwner(intent, route.BindingKey),
		"remote: 已配置 (" + route.Agent.Info().Type + ")",
	}
	return wechatCommandText(append(lines, h.claudeConfigStatus(route)...)...)
}

func renderClaudeBindingStatus(status claudeBindingStatus) string {
	switch status {
	case claudeBindingPendingResume:
		return "等待恢复"
	case claudeBindingReady:
		return "已就绪"
	case claudeBindingResumeFailed:
		return "恢复失败"
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
	views, err := h.claudeSwitchTargets(route)
	if err != nil {
		log.Printf("[claude-session] 查询会话列表失败: %v", err)
		return "查询 Claude 会话失败，请稍后重试。"
	}
	if len(views) == 0 {
		return "当前还没有可切换的 Claude 会话。"
	}
	lines := []string{"Claude 会话:"}
	for index, view := range views {
		owner := renderClaudeControlOwner(h.ensureClaudeSessions().controlIntent(view.ThreadID), route.BindingKey)
		lines = append(lines, fmt.Sprintf("%d. %s · 控制方: %s", index, claudeSessionListLabel(view), owner))
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
		lines = append(lines, fmt.Sprintf("%d. %s", index, group.Name))
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
		"/cc ls 查看可切换会话",
		"/cc cd <编号|..> 进入工作空间或返回列表",
		"/cc switch <编号|sessionId> 切换 Claude 会话",
		"/cc new 新建当前工作空间会话",
		"/cc pwd 查看当前工作空间",
		"/cc cli 打开本地 CLI 接手当前 session",
		"/cc status 查看 Claude session 状态",
		"/cc owner [remote|local] 查看、接管或释放控制权",
		"释放为 local 后普通消息会被拒绝；重新接管前请先结束本地 Claude CLI",
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
