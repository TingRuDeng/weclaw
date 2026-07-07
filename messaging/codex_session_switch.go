package messaging

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type codexSwitchOptions struct {
	actorUserID string
	platform    platform.PlatformName
	accountID   string
	reply       platform.Replier
}

func (h *Handler) handleCodexNew(userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.handleCodexNewForRoute(userID, agentName, workspaceRoot, ag, "")
}

// handleCodexNewForRoute 只清理 route session 的 thread，避免飞书 thread 影响同一用户其他会话。
func (h *Handler) handleCodexNewForRoute(userID string, agentName string, workspaceRoot string, ag agent.Agent, ownerBindingKey string) string {
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	if codexAg, ok := ag.(agent.CodexThreadAgent); ok {
		codexAg.ClearCodexThread(conversationID)
	}
	bindingKey := codexBindingKey(userID, agentName)
	h.ensureCodexSessions().setPendingNew(bindingKey, workspaceRoot)
	h.setCodexActiveWorkspaceForRoute(bindingKey, ownerBindingKey, workspaceRoot)
	return wechatCommandText("已切换到新会话。", "workspace: "+workspaceRoot)
}

func (h *Handler) handleCodexSwitch(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent, target string) string {
	return h.handleCodexSwitchForRoute(ctx, userID, agentName, workspaceRoot, ag, target, "")
}

// handleCodexSwitchForRoute 切换 route session 的 thread，并同步真实用户当前工作空间。
func (h *Handler) handleCodexSwitchForRoute(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent, target string, ownerBindingKey string) string {
	return h.handleCodexSwitchForRouteWithOptions(ctx, userID, agentName, workspaceRoot, ag, target, ownerBindingKey, codexSwitchOptions{})
}

func (h *Handler) handleCodexSwitchForRouteWithOptions(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent, target string, ownerBindingKey string, opts codexSwitchOptions) string {
	codexAg, ok := ag.(agent.CodexThreadAgent)
	if !ok {
		return "当前 Codex Agent 不支持 thread 切换。"
	}
	bindingKey := codexBindingKey(userID, agentName)
	workspaceRoot, threadID, err := h.resolveCodexSwitchTarget(bindingKey, agentName, workspaceRoot, target, ag)
	if err != nil {
		return err.Error()
	}
	conversationID := buildCodexConversationID(userID, agentName, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	if err := codexAg.UseCodexThread(ctx, conversationID, threadID); err != nil {
		return renderCodexSwitchFailure(err)
	}
	h.switchCodexWorkspaceForRoute(firstNonBlank(opts.actorUserID, userID), userID, agentName, workspaceRoot, ag)
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
	h.setCodexActiveWorkspaceForRoute(bindingKey, ownerBindingKey, workspaceRoot)
	lines := []string{"已切换会话。", "工作空间: " + shortCodexWorkspaceName(workspaceRoot)}
	state, active, activeErr := h.startExternalCodexTaskIfActive(externalCodexTaskOptions{
		ctx:            ctx,
		actorUserID:    firstNonBlank(opts.actorUserID, userID),
		routeUserID:    userID,
		agentName:      agentName,
		agent:          ag,
		conversationID: conversationID,
		threadID:       threadID,
		platform:       opts.platform,
		accountID:      opts.accountID,
		reply:          opts.reply,
	})
	if active {
		lines = append(lines, renderExternalCodexActiveNotice(state)...)
	}
	lines = append(lines, renderExternalCodexStateReadError(activeErr)...)
	return wechatCommandText(lines...)
}

func (h *Handler) resolveCodexSwitchTarget(bindingKey string, agentName string, workspaceRoot string, target string, ag agent.Agent) (string, string, error) {
	target = strings.TrimSpace(target)
	if index, ok := parseCodexListIndex(target); ok {
		if view, ok := h.resolveCodexSessionByIndex(bindingKey, index); ok {
			return h.resolveCodexSessionView(agentName, view, ag)
		}
		if _, browsing := h.codexBrowseWorkspace(bindingKey); browsing {
			return "", "", fmt.Errorf("会话编号不存在，请先发送 /cx ls 查看当前工作空间会话。")
		}
		views := h.codexSwitchTargets(bindingKey)
		if index < 0 || index >= len(views) {
			return "", "", fmt.Errorf("编号不存在，请先发送 /cx ls 查看可切换会话。")
		}
		return h.resolveCodexSessionView(agentName, views[index], ag)
	}
	threadID := target
	workspaceRoot = h.resolveCodexSwitchWorkspace(bindingKey, agentName, workspaceRoot, threadID, ag)
	return workspaceRoot, threadID, nil
}

func (h *Handler) resolveCodexSessionView(agentName string, view codexWorkspaceView, ag agent.Agent) (string, string, error) {
	threadID := strings.TrimSpace(view.ThreadID)
	if threadID == "" || view.PendingNewThread {
		return "", "", fmt.Errorf("该编号当前没有可切换的会话。")
	}
	return normalizeCodexWorkspaceRoot(view.WorkspaceRoot), threadID, nil
}

func parseCodexListIndex(value string) (int, bool) {
	if strings.TrimSpace(value) == "" {
		return 0, false
	}
	index, err := strconv.Atoi(value)
	return index, err == nil
}

func (h *Handler) resolveCodexSwitchWorkspace(bindingKey string, agentName string, fallbackWorkspace string, threadID string, ag agent.Agent) string {
	workspaceRoot, ok := h.ensureCodexSessions().findWorkspaceByThread(bindingKey, threadID)
	if ok {
		return normalizeCodexWorkspaceRoot(workspaceRoot)
	}
	if localWorkspace, ok := h.findLocalCodexWorkspaceByThread(threadID); ok {
		return normalizeCodexWorkspaceRoot(localWorkspace)
	}
	return normalizeCodexWorkspaceRoot(fallbackWorkspace)
}

func renderCodexSwitchFailure(err error) string {
	if isCodexThreadStoreReadError(err) {
		return wechatCommandText(
			"切换会话失败。",
			"该 Codex 会话当前无法被微信接手。",
			"可发送 /cx app 在 Codex App 中打开当前工作空间，或发送 /cx new 创建微信侧新会话。",
		)
	}
	return fmt.Sprintf("切换线程失败: %v", err)
}

func isCodexThreadStoreReadError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "thread-store internal error") ||
		strings.Contains(text, "failed to read thread") ||
		strings.Contains(text, "does not start with session metadata")
}
