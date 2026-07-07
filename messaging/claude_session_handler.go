package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

func isClaudeSessionCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || !isClaudeSessionCommandToken(fields[0]) {
		return false
	}
	switch fields[1] {
	case "whoami", "ls", "new", "switch", "pwd", "status", "cli", "model", "help":
		return true
	default:
		return false
	}
}

func isClaudeSessionCommandToken(token string) bool {
	return token == "/cc"
}

func (h *Handler) handleClaudeSessionCommand(ctx context.Context, userID string, trimmed string) string {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || fields[1] == "help" {
		return buildClaudeSessionHelpText()
	}
	agentName, ag, err := h.getClaudeSessionAgent(ctx)
	if err != nil {
		return err.Error()
	}
	workspaceRoot := h.claudeWorkspaceRootForUser(userID, agentName, ag)
	bindingKey := claudeBindingKey(userID, agentName)
	h.ensureClaudeSessions().ensureWorkspace(bindingKey, workspaceRoot)
	route := claudeSessionRoute{
		Context:       ctx,
		UserID:        userID,
		AgentName:     agentName,
		Agent:         ag,
		WorkspaceRoot: workspaceRoot,
		BindingKey:    bindingKey,
	}
	h.syncClaudeSessionFromAgent(route)
	return h.routeClaudeSessionCommand(fields, route)
}

type claudeSessionRoute struct {
	Context       context.Context
	UserID        string
	AgentName     string
	Agent         agent.Agent
	WorkspaceRoot string
	BindingKey    string
}

func (h *Handler) routeClaudeSessionCommand(fields []string, route claudeSessionRoute) string {
	switch fields[1] {
	case "whoami":
		return h.renderClaudeWhoami(route.BindingKey, route.WorkspaceRoot)
	case "ls":
		return h.renderClaudeWorkspaceList(route.BindingKey)
	case "pwd":
		return wechatCommandText("workspace: " + route.WorkspaceRoot)
	case "status":
		return h.renderClaudeStatus(route)
	case "cli":
		return h.handleClaudeCLI(route)
	case "model":
		return h.handleClaudeModelCommand(route.Context, route.Agent, fields[2:])
	case "new":
		return h.handleClaudeNew(route)
	case "switch":
		if len(fields) != 3 {
			return "用法: /cc switch <编号|sessionId>"
		}
		return h.handleClaudeSwitch(route, fields[2])
	default:
		return buildClaudeSessionHelpText()
	}
}

func (h *Handler) getClaudeSessionAgent(ctx context.Context) (string, agent.Agent, error) {
	agentName, ok := h.claudeAgentName()
	if !ok {
		return "", nil, fmt.Errorf("当前没有配置 Claude Agent。")
	}
	ag, err := h.getAgent(ctx, agentName)
	if err != nil {
		return "", nil, fmt.Errorf("Claude Agent 不可用: %v", err)
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
