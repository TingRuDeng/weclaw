package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// codexSessionCommandRequest 拆开真实用户和会话路由，避免飞书 thread 命令串到用户全局会话。
type codexSessionCommandRequest struct {
	ActorUserID string
	RouteUserID string
	Trimmed     string
	Platform    platform.PlatformName
	Reply       platform.Replier
}

func (h *Handler) handleCodexSessionCommand(ctx context.Context, userID string, trimmed string) string {
	return h.handleCodexSessionCommandForRoute(ctx, codexSessionCommandRequest{
		ActorUserID: userID,
		RouteUserID: userID,
		Trimmed:     trimmed,
	})
}

// handleCodexSessionCommandForRoute 让飞书内置会话命令操作 route session，同时继续按真实用户解析工作空间。
func (h *Handler) handleCodexSessionCommandForRoute(ctx context.Context, req codexSessionCommandRequest) string {
	actorUserID := strings.TrimSpace(req.ActorUserID)
	routeUserID := strings.TrimSpace(req.RouteUserID)
	if routeUserID == "" {
		routeUserID = actorUserID
	}
	if actorUserID == "" {
		actorUserID = routeUserID
	}
	trimmed := req.Trimmed
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || fields[1] == "help" {
		return buildCodexSessionHelpText()
	}
	if fields[1] == "model" && isCodexModelStatusArgs(fields[2:]) {
		return h.renderCodexModelStatusFromConfig()
	}

	agentName, ag, err := h.getCodexSessionAgent(ctx)
	if err != nil {
		return err.Error()
	}
	workspaceRoot := h.codexWorkspaceRootForRoute(actorUserID, routeUserID, agentName, ag)
	bindingKey := codexBindingKey(routeUserID, agentName)
	ownerBindingKey := codexBindingKey(actorUserID, agentName)
	h.ensureCodexSessions().ensureWorkspace(bindingKey, workspaceRoot)
	h.syncCodexThreadFromAgent(routeUserID, agentName, workspaceRoot, ag)

	if len(fields) == 2 && isCodexShortSelectionToken(fields[1]) {
		return h.handleCodexShortSelection(ctx, codexShortSelectionRequest{
			UserID:          routeUserID,
			ActorUserID:     actorUserID,
			AgentName:       agentName,
			WorkspaceRoot:   workspaceRoot,
			Agent:           ag,
			BindingKey:      bindingKey,
			Target:          fields[1],
			OwnerBindingKey: ownerBindingKey,
			Platform:        req.Platform,
			Reply:           req.Reply,
		})
	}

	switch fields[1] {
	case "whoami":
		return h.renderCodexWhoami(bindingKey, workspaceRoot)
	case "ls":
		return h.renderCodexList(bindingKey)
	case "cd":
		if len(fields) != 3 {
			return "用法: /cx cd <编号|工作空间名|..>"
		}
		return h.handleCodexCd(codexWorkspaceCdRequest{
			Context:         ctx,
			UserID:          routeUserID,
			ActorUserID:     actorUserID,
			BindingKey:      bindingKey,
			OwnerBindingKey: ownerBindingKey,
			AgentName:       agentName,
			Target:          fields[2],
			Agent:           ag,
			Platform:        req.Platform,
			Reply:           req.Reply,
		})
	case "pwd":
		return h.renderCodexPwd(bindingKey)
	case "status":
		if len(fields) != 2 {
			return "用法: /cx status"
		}
		return h.renderCodexStatusForRoute(actorUserID, routeUserID, agentName, workspaceRoot, ag)
	case "quota":
		if len(fields) != 2 {
			return "用法: /cx quota"
		}
		return h.renderCodexQuota(ctx, ag)
	case "clean":
		if len(fields) != 2 {
			return "用法: /cx clean"
		}
		return h.handleCodexClean(bindingKey)
	case "app", "open-app":
		if len(fields) != 2 {
			return "用法: /cx app"
		}
		return h.handleCodexOpenAppForRoute(ctx, actorUserID, routeUserID, agentName, workspaceRoot, ag)
	case "cli":
		if len(fields) != 2 {
			return "用法: /cx cli"
		}
		return h.handleCodexCLIForRoute(ctx, actorUserID, routeUserID, agentName, workspaceRoot, ag)
	case "attach":
		if len(fields) == 3 && fields[2] == "app" {
			return h.handleCodexOpenAppForRoute(ctx, actorUserID, routeUserID, agentName, workspaceRoot, ag)
		}
		if len(fields) != 2 {
			return "用法: /cx attach 或 /cx attach app"
		}
		return h.handleCodexAttachForRoute(ctx, actorUserID, routeUserID, agentName, workspaceRoot, ag)
	case "detach":
		if len(fields) != 2 {
			return "用法: /cx detach"
		}
		return h.handleCodexDetach(ag)
	case "model":
		return h.handleCodexModelCommand(ctx, ag, fields[2:])
	case "new":
		return h.handleCodexNewForRoute(routeUserID, agentName, workspaceRoot, ag, ownerBindingKey)
	case "new-thread":
		return h.handleCodexNewForRoute(routeUserID, agentName, workspaceRoot, ag, ownerBindingKey)
	case "switch":
		if len(fields) != 3 {
			return "用法: /cx switch <编号|threadId>"
		}
		return h.handleCodexSwitchForRouteWithOptions(ctx, routeUserID, agentName, workspaceRoot, ag, fields[2], ownerBindingKey, codexSwitchOptions{
			actorUserID: actorUserID,
			platform:    req.Platform,
			reply:       req.Reply,
		})
	default:
		return buildCodexSessionHelpText()
	}
}

type codexShortSelectionRequest struct {
	UserID          string
	ActorUserID     string
	AgentName       string
	WorkspaceRoot   string
	Agent           agent.Agent
	BindingKey      string
	Target          string
	OwnerBindingKey string
	Platform        platform.PlatformName
	Reply           platform.Replier
}

func (h *Handler) handleCodexShortSelection(ctx context.Context, req codexShortSelectionRequest) string {
	if req.Target == ".." {
		return h.handleCodexCd(codexWorkspaceCdRequest{
			Context:         ctx,
			UserID:          req.UserID,
			ActorUserID:     req.ActorUserID,
			BindingKey:      req.BindingKey,
			OwnerBindingKey: req.OwnerBindingKey,
			AgentName:       req.AgentName,
			Target:          req.Target,
			Agent:           req.Agent,
			Platform:        req.Platform,
			Reply:           req.Reply,
		})
	}
	if _, browsing := h.codexBrowseWorkspace(req.BindingKey); browsing {
		return h.handleCodexSwitchForRouteWithOptions(ctx, req.UserID, req.AgentName, req.WorkspaceRoot, req.Agent, req.Target, req.OwnerBindingKey, codexSwitchOptions{
			actorUserID: req.ActorUserID,
			platform:    req.Platform,
			reply:       req.Reply,
		})
	}
	return h.handleCodexCd(codexWorkspaceCdRequest{
		Context:         ctx,
		UserID:          req.UserID,
		ActorUserID:     req.ActorUserID,
		BindingKey:      req.BindingKey,
		OwnerBindingKey: req.OwnerBindingKey,
		AgentName:       req.AgentName,
		Target:          req.Target,
		Agent:           req.Agent,
		Platform:        req.Platform,
		Reply:           req.Reply,
	})
}

func (h *Handler) handleCodexClean(bindingKey string) string {
	removed := h.ensureCodexSessions().cleanMissingWorkspaces(bindingKey)
	if len(removed) == 0 {
		return "没有需要清理的 Codex 工作空间。"
	}
	if browsing, ok := h.codexBrowseWorkspace(bindingKey); ok && containsWorkspaceRoot(removed, browsing) {
		h.clearCodexBrowseWorkspace(bindingKey)
	}
	names := make([]string, 0, len(removed))
	for _, root := range removed {
		names = append(names, shortCodexWorkspaceName(root))
	}
	return wechatCommandText(
		fmt.Sprintf("已清理 Codex 工作空间：%d 个", len(removed)),
		"已移除："+strings.Join(names, "、"),
		"未删除 Codex App 历史文件。",
	)
}

func containsWorkspaceRoot(roots []string, target string) bool {
	target = normalizeCodexWorkspaceRoot(target)
	for _, root := range roots {
		if normalizeCodexWorkspaceRoot(root) == target {
			return true
		}
	}
	return false
}
