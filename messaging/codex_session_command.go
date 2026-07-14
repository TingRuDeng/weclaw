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
	AccountID   string
	Reply       platform.Replier
	Admin       bool
}

// handleCodexSessionCommandForRoute 让飞书内置会话命令操作 route session，同时继续按真实用户解析工作空间。
func (h *Handler) handleCodexSessionCommandForRoute(ctx context.Context, req codexSessionCommandRequest) string {
	return h.handleCodexSessionCommandForRouteResult(ctx, req).Reply
}

// handleCodexSessionCommandForRouteResult 执行命令并显式标记是否可展示导航卡片。
func (h *Handler) handleCodexSessionCommandForRouteResult(ctx context.Context, req codexSessionCommandRequest) navigationCommandResult {
	prepared := h.prepareCodexSessionCommand(ctx, req)
	if !prepared.ready {
		return prepared.result
	}
	defer prepared.unlock()
	return h.dispatchCodexSessionCommand(prepared.runtime)
}

func (h *Handler) rejectDisallowedCodexWorkspace(bindingKey string, agentName string, workspaceRoot string, fields []string, admin bool) string {
	if admin || len(fields) < 2 {
		return ""
	}
	command := fields[1]
	if isCodexWorkspaceIndependentCommand(command) {
		return ""
	}
	if isCodexShortSelectionToken(command) {
		if browsing, ok := h.codexBrowseWorkspace(bindingKey); ok && !h.isWorkspaceAllowed(browsing) {
			h.clearCodexBrowseWorkspace(bindingKey)
			return "当前浏览工作空间不在允许范围，请发送 /cx ls 重新选择。"
		}
		return ""
	}
	if h.isWorkspaceAllowed(workspaceRoot) || h.isConfiguredAgentWorkspace(agentName, workspaceRoot) {
		return ""
	}
	return "当前工作空间不在允许范围，请发送 /cx ls 重新选择。"
}

func isCodexWorkspaceIndependentCommand(command string) bool {
	switch command {
	case "ls", "cd", "clean", "quota", "detach", "model":
		return true
	default:
		return false
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
	AccountID       string
	Reply           platform.Replier
	Admin           bool
}

// handleCodexShortSelection 保留短编号工作空间导航的结构化卡片状态。
func (h *Handler) handleCodexShortSelection(ctx context.Context, req codexShortSelectionRequest) navigationCommandResult {
	if req.Target == ".." {
		return h.handleCodexCdResult(codexWorkspaceCdRequest{
			Context:         ctx,
			UserID:          req.UserID,
			ActorUserID:     req.ActorUserID,
			BindingKey:      req.BindingKey,
			OwnerBindingKey: req.OwnerBindingKey,
			AgentName:       req.AgentName,
			Target:          req.Target,
			Agent:           req.Agent,
			Platform:        req.Platform,
			AccountID:       req.AccountID,
			Reply:           req.Reply,
			Admin:           req.Admin,
		})
	}
	if _, browsing := h.codexBrowseWorkspace(req.BindingKey); browsing {
		return textNavigationResult(h.handleCodexSwitchForRouteWithOptions(ctx, req.UserID, req.AgentName, req.WorkspaceRoot, req.Agent, req.Target, req.OwnerBindingKey, codexSwitchOptions{
			actorUserID: req.ActorUserID,
			platform:    req.Platform,
			accountID:   req.AccountID,
			reply:       req.Reply,
		}))
	}
	return h.handleCodexCdResult(codexWorkspaceCdRequest{
		Context:         ctx,
		UserID:          req.UserID,
		ActorUserID:     req.ActorUserID,
		BindingKey:      req.BindingKey,
		OwnerBindingKey: req.OwnerBindingKey,
		AgentName:       req.AgentName,
		Target:          req.Target,
		Agent:           req.Agent,
		Platform:        req.Platform,
		AccountID:       req.AccountID,
		Reply:           req.Reply,
		Admin:           req.Admin,
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
