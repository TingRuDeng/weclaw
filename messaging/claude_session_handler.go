package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

func isClaudeSessionCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || !isClaudeSessionCommandToken(fields[0]) {
		return false
	}
	switch fields[1] {
	case "whoami", "ls", "cd", "new", "switch", "pwd", "status", "owner", "cli", "model", "quota", "help", "page":
		return true
	default:
		return false
	}
}

func isClaudeSessionCommandToken(token string) bool {
	return token == "/cc"
}

func (h *Handler) handleClaudeSessionCommand(ctx context.Context, userID string, trimmed string) string {
	return h.handleClaudeSessionCommandForRoute(ctx, userID, userID, h.isAdminUser(userID), trimmed)
}

func (h *Handler) handleClaudeSessionCommandForRoute(ctx context.Context, actorUserID string, routeUserID string, admin bool, trimmed string) string {
	return h.handleClaudeSessionCommandForRouteResult(ctx, actorUserID, routeUserID, admin, trimmed).Reply
}

// handleClaudeSessionCommandForRouteResult 执行命令并显式标记是否可展示导航卡片。
func (h *Handler) handleClaudeSessionCommandForRouteResult(ctx context.Context, actorUserID string, routeUserID string, admin bool, trimmed string) navigationCommandResult {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || fields[1] == "help" {
		return textNavigationResult(buildClaudeSessionHelpText())
	}
	agentName, ag, err := h.getClaudeSessionAgent(ctx)
	if err != nil {
		log.Printf("[claude-session] 获取 Claude Agent 失败: %v", err)
		return textNavigationResult("Claude Agent 当前不可用，请稍后重试。")
	}
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = actorUserID
	}
	workspaceRoot := h.claudeWorkspaceRootForUser(routeUserID, agentName, ag)
	bindingKey := claudeBindingKey(routeUserID, agentName)
	route := claudeSessionRoute{
		Context:       ctx,
		ActorUserID:   actorUserID,
		UserID:        routeUserID,
		AgentName:     agentName,
		Agent:         ag,
		WorkspaceRoot: workspaceRoot,
		BindingKey:    bindingKey,
		Admin:         admin,
	}
	return h.routeClaudeSessionCommand(fields, route)
}

type claudeSessionRoute struct {
	Context       context.Context
	ActorUserID   string
	UserID        string
	AgentName     string
	Agent         agent.Agent
	WorkspaceRoot string
	BindingKey    string
	Admin         bool
}

func (h *Handler) routeClaudeSessionCommand(fields []string, route claudeSessionRoute) navigationCommandResult {
	switch fields[1] {
	case "whoami":
		return textNavigationResult(h.renderClaudeWhoami(route))
	case "ls":
		return cardNavigationResult(h.renderClaudeWorkspaceList(route))
	case "cd":
		if len(fields) != 3 {
			return textNavigationResult("用法: /cc cd <工作空间编号|..>")
		}
		return h.handleClaudeCdResult(route, fields[2])
	case "pwd":
		return textNavigationResult(wechatCommandText("workspace: " + route.WorkspaceRoot))
	case "status":
		return textNavigationResult(h.renderClaudeStatus(route))
	case "owner":
		return textNavigationResult(h.handleClaudeOwnerCommand(route, fields[2:]))
	case "cli":
		return textNavigationResult(h.handleClaudeCLI(route))
	case "model":
		return textNavigationResult(h.handleClaudeModelCommand(route.Context, route.Agent, fields[2:]))
	case "quota":
		return textNavigationResult(h.renderClaudeQuota(route.Context, route.Agent))
	case "new":
		return textNavigationResult(h.handleClaudeNew(route))
	case "switch":
		if len(fields) != 3 {
			return textNavigationResult("用法: /cc switch <编号|sessionId>")
		}
		return textNavigationResult(h.handleClaudeSwitch(route, fields[2]))
	default:
		return textNavigationResult(buildClaudeSessionHelpText())
	}
}

func (h *Handler) getClaudeSessionAgent(ctx context.Context) (string, agent.Agent, error) {
	agentName, ok := h.claudeAgentName()
	if !ok {
		return "", nil, fmt.Errorf("当前没有配置 claude agent")
	}
	ag, err := h.getAgent(ctx, agentName)
	if err != nil {
		return "", nil, fmt.Errorf("claude agent 不可用: %v", err)
	}
	return agentName, ag, nil
}

func (h *Handler) claudeAgentName() (string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if ag, ok := h.agents["claude"]; ok && isClaudeAgent("claude", ag.Info()) {
		return "claude", true
	}
	if h.defaultName != "" {
		if ag, ok := h.agents[h.defaultName]; ok && isClaudeAgent(h.defaultName, ag.Info()) {
			return h.defaultName, true
		}
	}
	for _, meta := range h.agentMetas {
		info := agent.AgentInfo{Name: meta.Name, Type: meta.Type, Command: meta.Command}
		if meta.Name == "claude" || isClaudeAgent(meta.Name, info) {
			return meta.Name, true
		}
	}
	return "", false
}
