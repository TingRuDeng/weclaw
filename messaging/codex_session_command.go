package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// codexSessionCommandRequest 拆开真实用户和会话路由，避免飞书 thread 命令串到用户全局会话。
type codexSessionCommandRequest struct {
	ActorUserID string
	RouteUserID string
	Trimmed     string
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
	workspaceRoot := h.codexWorkspaceRootForUser(actorUserID, agentName, ag)
	bindingKey := codexBindingKey(routeUserID, agentName)
	ownerBindingKey := codexBindingKey(actorUserID, agentName)
	h.ensureCodexSessions().ensureWorkspace(bindingKey, workspaceRoot)
	h.syncCodexThreadFromAgent(routeUserID, agentName, workspaceRoot, ag)

	if len(fields) == 2 && isCodexShortSelectionToken(fields[1]) {
		return h.handleCodexShortSelection(ctx, routeUserID, agentName, workspaceRoot, ag, bindingKey, fields[1], ownerBindingKey)
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
			BindingKey:      bindingKey,
			OwnerBindingKey: ownerBindingKey,
			AgentName:       agentName,
			Target:          fields[2],
			Agent:           ag,
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
	case "switch":
		if len(fields) != 3 {
			return "用法: /cx switch <编号|threadId>"
		}
		return h.handleCodexSwitchForRoute(ctx, routeUserID, agentName, workspaceRoot, ag, fields[2], ownerBindingKey)
	default:
		return buildCodexSessionHelpText()
	}
}

func (h *Handler) handleCodexShortSelection(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent, bindingKey string, target string, ownerBindingKey string) string {
	if target == ".." {
		return h.handleCodexCd(codexWorkspaceCdRequest{
			Context:         ctx,
			UserID:          userID,
			BindingKey:      bindingKey,
			OwnerBindingKey: ownerBindingKey,
			AgentName:       agentName,
			Target:          target,
			Agent:           ag,
		})
	}
	if _, browsing := h.codexBrowseWorkspace(bindingKey); browsing {
		return h.handleCodexSwitchForRoute(ctx, userID, agentName, workspaceRoot, ag, target, ownerBindingKey)
	}
	return h.handleCodexCd(codexWorkspaceCdRequest{
		Context:         ctx,
		UserID:          userID,
		BindingKey:      bindingKey,
		OwnerBindingKey: ownerBindingKey,
		AgentName:       agentName,
		Target:          target,
		Agent:           ag,
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
