package messaging

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

func (h *Handler) renderClaudeWhoami(bindingKey string, workspaceRoot string) string {
	sessionID, pending := h.ensureClaudeSessions().getSession(bindingKey, workspaceRoot)
	return wechatCommandText("workspace: "+workspaceRoot, "session: "+renderCodexThreadLabel(sessionID, pending))
}

func (h *Handler) renderClaudeStatus(route claudeSessionRoute) string {
	workspaceRoot := h.claudeWorkspaceRootForUser(route.UserID, route.AgentName, route.Agent)
	route.WorkspaceRoot = workspaceRoot
	h.syncClaudeSessionFromAgent(route)
	sessionID, pending := h.ensureClaudeSessions().getSession(route.BindingKey, workspaceRoot)
	return wechatCommandText(
		"Claude 状态:",
		"工作空间: "+workspaceRoot,
		"session: "+renderCodexThreadLabel(sessionID, pending),
		"remote: 已配置 ("+route.Agent.Info().Type+")",
	)
}

func (h *Handler) renderClaudeWorkspaceList(bindingKey string) string {
	views := h.claudeSwitchTargets(bindingKey)
	if len(views) == 0 {
		return "当前还没有可切换的 Claude 会话。"
	}
	lines := []string{"Claude 会话:"}
	for index, view := range views {
		lines = append(lines, fmt.Sprintf("%d. %s", index, claudeSessionListLabel(view)))
	}
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
		"/cc switch <编号|sessionId> 切换 Claude 会话",
		"/cc new 新建当前工作空间会话",
		"/cc pwd 查看当前工作空间",
		"/cc cli 打开本地 CLI 接手当前 session",
		"/cc status 查看 Claude session 状态",
		"/cc model ls 查看 Claude 可选模型",
	)
}

func isClaudeAgent(name string, info agent.AgentInfo) bool {
	if strings.EqualFold(name, "claude") || strings.EqualFold(info.Name, "claude") {
		return true
	}
	command := strings.ToLower(info.Command)
	return strings.Contains(command, "claude")
}
