package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// switchDefault 切换当前消息会话的默认 Agent，并在目标 Agent 可用后持久化选择。
func (h *Handler) switchDefault(ctx context.Context, routeUserID string, name string) string {
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to switch default to %q: %v", name, err)
		return fmt.Sprintf("切换到 %q 失败：%v", name, err)
	}
	if err := h.ensureAgentSessions().Set(routeUserID, name); err != nil {
		log.Printf("[handler] failed to save session agent %q for %q: %v", name, routeUserID, err)
		return fmt.Sprintf("切换到 %q 失败：%v", name, err)
	}

	info := ag.Info()
	log.Printf("[handler] switched session %q to agent %s (%s)", routeUserID, name, info)
	return fmt.Sprintf("当前会话已切换到 %s", name)
}

type defaultSessionResetRequest struct {
	actorUserID string
	routeUserID string
	platform    platform.PlatformName
	accountID   string
	reply       platform.Replier
}

type claudeDefaultSessionResetRequest struct {
	ctx         context.Context
	actorUserID string
	userID      string
	agentName   string
	agent       agent.Agent
}

// resetDefaultSessionForMessage 按消息会话选择的 Agent 重置对应会话。
func (h *Handler) resetDefaultSessionForMessage(ctx context.Context, req defaultSessionResetRequest) string {
	actorUserID := req.actorUserID
	routeUserID := req.routeUserID
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = actorUserID
	}
	name := h.defaultAgentNameForRoute(routeUserID, req.platform, req.accountID)
	ag, err := h.getAgent(ctx, name)
	if err != nil || ag == nil {
		return "No agent running."
	}
	if isCodexAgent(name, ag.Info()) {
		return h.resetDefaultCodexSessionForRoute(ctx, defaultCodexSessionCreateRequest{
			actorUserID: actorUserID, routeUserID: routeUserID, agentName: name,
			agent: ag, platform: req.platform, accountID: req.accountID, reply: req.reply,
		})
	}
	if isClaudeAgent(name, ag.Info()) {
		return h.resetDefaultClaudeSession(claudeDefaultSessionResetRequest{
			ctx: ctx, actorUserID: actorUserID, userID: routeUserID, agentName: name, agent: ag,
		})
	}
	sessionID, err := ag.ResetSession(ctx, routeUserID)
	if err != nil {
		log.Printf("[handler] reset session failed for %s: %v", routeUserID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	if sessionID != "" {
		return wechatCommandText(fmt.Sprintf("已创建新的%s会话", name), sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

type defaultCodexSessionCreateRequest struct {
	actorUserID string
	routeUserID string
	agentName   string
	agent       agent.Agent
	platform    platform.PlatformName
	accountID   string
	reply       platform.Replier
}

// resetDefaultCodexSessionForRoute 按 route 当前工作空间创建并接管新的 Codex thread。
func (h *Handler) resetDefaultCodexSessionForRoute(ctx context.Context, req defaultCodexSessionCreateRequest) string {
	bindingKey := codexBindingKey(req.routeUserID, req.agentName)
	unlockBinding := h.lockAgentExecution(codexBindingExecutionKey(bindingKey))
	defer unlockBinding()
	workspaceRoot := h.codexWorkspaceRootForRoute(
		req.actorUserID, req.routeUserID, req.agentName, req.agent,
	)
	return h.handleCodexNewForRoute(codexNewRequest{
		ctx: ctx, taskContext: normalizeContext(ctx), actorUserID: req.actorUserID,
		userID: req.routeUserID, agentName: req.agentName,
		workspaceRoot: workspaceRoot, agent: req.agent,
		platform: req.platform, accountID: req.accountID, reply: req.reply,
	})
}

// resetDefaultClaudeSession 使用当前工作空间创建并绑定新的 Claude ACP session。
func (h *Handler) resetDefaultClaudeSession(req claudeDefaultSessionResetRequest) string {
	workspaceRoot := h.claudeWorkspaceRootForUser(req.userID, req.agentName, req.agent)
	route := claudeSessionRoute{
		Context: req.ctx, ActorUserID: req.actorUserID, UserID: req.userID, AgentName: req.agentName,
		Agent: req.agent, WorkspaceRoot: workspaceRoot, BindingKey: claudeBindingKey(req.userID, req.agentName),
		Admin: h.isAdminUser(req.actorUserID),
	}
	return h.handleClaudeNew(route)
}
