package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func isClaudeSessionCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || !isClaudeSessionCommandToken(fields[0]) {
		return false
	}
	switch fields[1] {
	case "whoami", "ls", "new", "pwd", "status", "cli", "quota", "help":
		return len(fields) == 2
	case "workspace":
		return len(fields) >= 3
	case "session":
		return len(fields) >= 3
	case "rename":
		return true
	case "cd", "switch":
		// 缺少目标时仍进入命令处理并返回用法；多余内容保留给 /cc 消息别名。
		return len(fields) == 2 || len(fields) == 3
	case "owner":
		return len(fields) == 2 || len(fields) == 3 && (strings.EqualFold(fields[2], "remote") || strings.EqualFold(fields[2], "local"))
	case "model":
		return len(fields) == 2 || len(fields) == 3 &&
			(fields[2] == "status" || fields[2] == "ls" || fields[2] == "reset")
	case "page":
		_, ok := parseFeishuNavigationPage(fields, "/cc")
		return ok
	default:
		return false
	}
}

func isClaudeSessionCommandToken(token string) bool {
	return token == "/cc"
}

func (h *Handler) handleClaudeSessionCommand(ctx context.Context, userID string, trimmed string) string {
	return h.handleClaudeSessionCommandForRouteRequest(ctx, claudeSessionCommandRequest{
		ActorUserID: userID, RouteUserID: userID, Trimmed: trimmed,
		Admin: false, Private: true,
	}).Reply
}

// handleClaudeSessionCommandForRouteResult 执行命令并显式标记是否可展示导航卡片。
func (h *Handler) handleClaudeSessionCommandForRouteResult(ctx context.Context, actorUserID string, routeUserID string, admin bool, trimmed string) navigationCommandResult {
	return h.handleClaudeSessionCommandForRouteRequest(ctx, claudeSessionCommandRequest{
		ActorUserID: actorUserID, RouteUserID: routeUserID, Trimmed: trimmed, Admin: admin, Private: true,
	})
}

type claudeSessionCommandRequest struct {
	ActorUserID string
	RouteUserID string
	Trimmed     string
	Platform    platform.PlatformName
	Admin       bool
	Private     bool
}

func (h *Handler) handleClaudeSessionCommandForRouteRequest(ctx context.Context, req claudeSessionCommandRequest) navigationCommandResult {
	actorUserID := strings.TrimSpace(req.ActorUserID)
	routeUserID := strings.TrimSpace(req.RouteUserID)
	if routeUserID == "" {
		routeUserID = actorUserID
	}
	if spec, handled, parseErr := parseSessionVisibilityCommand(req.Trimmed, "/cc"); handled {
		if parseErr != nil {
			return textNavigationResult(parseErr.Error())
		}
		agentName, ok := h.claudeAgentName()
		if !ok {
			return textNavigationResult("当前没有配置 claude agent")
		}
		if !req.Admin {
			return textNavigationResult("当前账号未授权隐藏或恢复主机级会话导航。")
		}
		if !req.Private {
			return textNavigationResult("会话隐藏与恢复只允许在私聊中执行。")
		}
		var ag agent.Agent
		if spec.Action == "remove" {
			var err error
			_, ag, err = h.getClaudeSessionAgent(ctx)
			if err != nil {
				log.Printf("[claude-session] 获取 Claude Agent 失败: %v", err)
				return textNavigationResult("Claude Agent 当前不可用，请稍后重试。")
			}
		}
		return textNavigationResult(h.handleSessionVisibilityCommand(sessionVisibilityCommandRequest{
			Context: ctx, ActorUserID: actorUserID, RouteUserID: routeUserID,
			AgentName: agentName, AgentKind: "claude", BindingKey: claudeBindingKey(routeUserID, agentName), Agent: ag,
			Platform: req.Platform, Admin: req.Admin, Private: req.Private, Spec: spec,
		}))
	}
	if spec, handled, parseErr := parseWorkspaceCommand(req.Trimmed, "/cc"); handled {
		if parseErr != nil {
			return textNavigationResult(parseErr.Error())
		}
		agentName, ok := h.claudeAgentName()
		if !ok {
			return textNavigationResult("当前没有配置 claude agent")
		}
		return textNavigationResult(h.handleWorkspaceCommand(workspaceCommandRequest{
			Context: ctx, ActorUserID: actorUserID, RouteUserID: routeUserID,
			AgentName: agentName, AgentKind: "claude", BindingKey: claudeBindingKey(routeUserID, agentName),
			Platform: req.Platform, Admin: req.Admin, Private: req.Private, Spec: spec,
		}))
	}
	renameSpec, renameHandled, renameErr := parseSessionRenameCommand(req.Trimmed, "/cc")
	if renameHandled && renameErr != nil {
		return textNavigationResult(renameErr.Error())
	}
	fields := strings.Fields(req.Trimmed)
	if len(fields) < 2 || fields[1] == "help" {
		return textNavigationResult(buildClaudeSessionHelpText())
	}
	agentName, ag, err := h.getClaudeSessionAgent(ctx)
	if err != nil {
		log.Printf("[claude-session] 获取 Claude Agent 失败: %v", err)
		return textNavigationResult("Claude Agent 当前不可用，请稍后重试。")
	}
	unlockRegistry := func() {}
	if claudeCommandRequiresWorkspaceRegistryControl(fields[1]) {
		unlockRegistry = h.lockWorkspaceRegistryControl()
	}
	defer unlockRegistry()
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
		Admin:         req.Admin,
		Platform:      req.Platform,
	}
	if renameHandled {
		route.RenameSpec = &renameSpec
	}
	return h.routeClaudeSessionCommand(fields, route)
}

func claudeCommandRequiresWorkspaceRegistryControl(command string) bool {
	switch command {
	case "cd", "new", "switch", "rename":
		return true
	default:
		return false
	}
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
	Platform      platform.PlatformName
	RenameSpec    *sessionRenameCommandSpec
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
		return textNavigationResult(claudeSingleHostEntryDisabled("owner"))
	case "cli":
		return textNavigationResult(h.handleClaudeCLI(route))
	case "model":
		return textNavigationResult(h.handleClaudeModelCommand(route.Context, route.Agent, fields[2:]))
	case "quota":
		return textNavigationResult(h.renderClaudeQuota(route.Context, route.Agent))
	case "new":
		return h.handleClaudeNewResult(route)
	case "switch":
		if len(fields) != 3 {
			return textNavigationResult("用法: /cc switch <编号|sessionId>")
		}
		return textNavigationResult(h.handleClaudeSwitch(route, fields[2]))
	case "rename":
		return textNavigationResult(h.handleClaudeRename(route))
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
