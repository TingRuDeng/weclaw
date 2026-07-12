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

type codexNewRequest struct {
	ctx             context.Context
	userID          string
	agentName       string
	workspaceRoot   string
	agent           agent.Agent
	ownerBindingKey string
}

// handleCodexNewForRoute 立即创建 route session 的 thread，避免依赖下一条普通消息隐式创建。
func (h *Handler) handleCodexNewForRoute(req codexNewRequest) string {
	conversationID := buildCodexConversationID(req.userID, req.agentName, req.workspaceRoot)
	h.bindConversationCwd(req.agent, conversationID, req.workspaceRoot)
	threadID, err := req.agent.ResetSession(req.ctx, conversationID)
	if err != nil {
		return fmt.Sprintf("创建新的 Codex 会话失败: %v", err)
	}
	bindingKey := codexBindingKey(req.userID, req.agentName)
	h.recordResetCodexThread(req.userID, req.agentName, req.workspaceRoot, threadID)
	h.setCodexActiveWorkspaceForRoute(bindingKey, req.ownerBindingKey, req.workspaceRoot)
	return wechatCommandText("已创建新的"+req.agentName+"会话", threadID)
}

func (h *Handler) handleCodexSwitchForRouteWithOptions(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent, target string, ownerBindingKey string, opts codexSwitchOptions) string {
	_, ok := ag.(agent.CodexThreadAgent)
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
	route := codexConversationRoute{
		bindingKey: bindingKey, workspaceRoot: workspaceRoot,
		conversationID: conversationID, threadID: threadID,
	}
	resolution, err := h.resolveCodexRuntime(ctx, codexRuntimeResolveOptions{
		route: route, threadID: threadID, ag: ag, allowDisconnectedRecovery: true,
	})
	if err != nil {
		return renderCodexSwitchFailure(err)
	}
	h.switchCodexWorkspaceForRoute(firstNonBlank(opts.actorUserID, userID), userID, agentName, workspaceRoot, ag)
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
	h.setCodexActiveWorkspaceForRoute(bindingKey, ownerBindingKey, workspaceRoot)
	lines := []string{"已切换会话。", "工作空间: " + shortCodexWorkspaceName(workspaceRoot)}
	modelStatus := codexResolutionModelStatus(resolution, h.codexSessionModelStatus(threadID))
	lines = append(lines, renderSessionModelStatus(modelStatus)...)
	lines = append(lines, renderCodexOwnerNotice(resolution)...)
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
