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
	actorUserID     string
	platform        platform.PlatformName
	accountID       string
	reply           platform.Replier
	externalTaskCtx context.Context
}

type codexSwitchRequest struct {
	ctx             context.Context
	userID          string
	agentName       string
	workspaceRoot   string
	agent           agent.Agent
	target          string
	ownerBindingKey string
	options         codexSwitchOptions
}

type codexSwitchTargetRequest struct {
	bindingKey    string
	agentName     string
	workspaceRoot string
	target        string
	agent         agent.Agent
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
	if err := h.ensureAgentSessions().Set(req.userID, req.agentName); err != nil {
		return fmt.Sprintf("创建新的 Codex 会话失败: 保存当前窗口 Agent: %v", err)
	}
	return wechatCommandText("已创建新的"+req.agentName+"会话", threadID)
}

func (h *Handler) handleCodexSwitchForRouteWithOptions(req codexSwitchRequest) string {
	if _, ok := req.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return "当前 Codex Agent 不支持选择即接管。"
	}
	route, err := h.resolveCodexSwitchRoute(req)
	if err != nil {
		return err.Error()
	}
	result, err := h.acquireCodexSessionWithBindingLocked(req.acquireRequest(route))
	if err != nil {
		return renderCodexSessionAcquireFailure(err)
	}
	return h.renderCodexSessionAcquireSuccess(result)
}

func (h *Handler) resolveCodexSwitchRoute(req codexSwitchRequest) (codexConversationRoute, error) {
	bindingKey := codexBindingKey(req.userID, req.agentName)
	workspaceRoot, threadID, err := h.resolveCodexSwitchTarget(codexSwitchTargetRequest{
		bindingKey: bindingKey, agentName: req.agentName,
		workspaceRoot: req.workspaceRoot, target: req.target, agent: req.agent,
	})
	if err != nil {
		return codexConversationRoute{}, err
	}
	return codexConversationRoute{
		bindingKey: bindingKey, workspaceRoot: workspaceRoot,
		conversationID: buildCodexConversationID(req.userID, req.agentName, workspaceRoot),
		threadID:       threadID,
	}, nil
}

func (req codexSwitchRequest) acquireRequest(route codexConversationRoute) codexSessionAcquireRequest {
	return codexSessionAcquireRequest{
		ctx: req.ctx, taskContext: codexExternalTaskContext(req),
		actorUserID: firstNonBlank(req.options.actorUserID, req.userID),
		routeUserID: req.userID, agentName: req.agentName, agent: req.agent,
		route: route, platform: req.options.platform, accountID: req.options.accountID,
		reply: req.options.reply,
	}
}

// renderCodexSessionAcquireSuccess 只在事务完整提交后宣告切换与接管成功。
func (h *Handler) renderCodexSessionAcquireSuccess(result codexSessionAcquireResult) string {
	return h.renderCodexSessionAcquireResult(
		result, "已切换并接管。", shortCodexWorkspaceName(result.route.workspaceRoot),
	)
}

// renderCodexSessionAcquireResult 统一展示已提交事务的控制方、运行位置与观察状态。
func (h *Handler) renderCodexSessionAcquireResult(result codexSessionAcquireResult, headline string, workspaceName string) string {
	lines := []string{headline, "工作空间: " + workspaceName}
	modelStatus := codexResolutionModelStatus(
		result.resolution, h.codexSessionModelStatus(result.route.threadID),
	)
	lines = append(lines, renderSessionModelStatus(modelStatus)...)
	lines = append(lines,
		"控制方: 当前远程窗口",
		"运行位置: "+renderCodexRuntimeHolder(result.resolution.Binding.Runtime),
	)
	if result.externalActive {
		lines = append(lines, "已开始回传当前任务的进度和结果。")
		lines = append(lines, renderExternalCodexActiveNotice(result.externalState)...)
	}
	if result.agentSessionErr != nil {
		lines = append(lines, "警告: 保存当前窗口 Agent 失败: "+result.agentSessionErr.Error())
	}
	return wechatCommandText(lines...)
}

func codexExternalTaskContext(req codexSwitchRequest) context.Context {
	if req.options.externalTaskCtx != nil {
		return req.options.externalTaskCtx
	}
	return normalizeContext(req.ctx)
}

func (h *Handler) resolveCodexSwitchTarget(req codexSwitchTargetRequest) (string, string, error) {
	target := strings.TrimSpace(req.target)
	if index, ok := parseCodexListIndex(target); ok {
		if view, ok := h.resolveCodexSessionByIndex(req.bindingKey, index); ok {
			return h.resolveCodexSessionView(req.agentName, view, req.agent)
		}
		if _, browsing := h.codexBrowseWorkspace(req.bindingKey); browsing {
			return "", "", fmt.Errorf("会话编号不存在，请先发送 /cx ls 查看当前工作空间会话。")
		}
		views := h.codexSwitchTargets(req.bindingKey)
		if index < 0 || index >= len(views) {
			return "", "", fmt.Errorf("编号不存在，请先发送 /cx ls 查看可切换会话。")
		}
		return h.resolveCodexSessionView(req.agentName, views[index], req.agent)
	}
	threadID := target
	workspaceRoot := h.resolveCodexSwitchWorkspace(codexSwitchTargetRequest{
		bindingKey: req.bindingKey, agentName: req.agentName,
		workspaceRoot: req.workspaceRoot, target: threadID, agent: req.agent,
	})
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

func (h *Handler) resolveCodexSwitchWorkspace(req codexSwitchTargetRequest) string {
	workspaceRoot, ok := h.ensureCodexSessions().findWorkspaceByThread(req.bindingKey, req.target)
	if ok {
		return normalizeCodexWorkspaceRoot(workspaceRoot)
	}
	if localWorkspace, ok := h.findLocalCodexWorkspaceByThread(req.target); ok {
		return normalizeCodexWorkspaceRoot(localWorkspace)
	}
	return normalizeCodexWorkspaceRoot(req.workspaceRoot)
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
