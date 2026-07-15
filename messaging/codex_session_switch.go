package messaging

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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

type codexSwitchRenderRequest struct {
	switchRequest codexSwitchRequest
	route         codexConversationRoute
	resolution    codexRuntimeResolution
	runtimeErr    error
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
	_, ok := req.agent.(agent.CodexThreadAgent)
	if !ok {
		return "当前 Codex Agent 不支持 thread 切换。"
	}
	route, err := h.resolveCodexSwitchRoute(req)
	if err != nil {
		return err.Error()
	}
	if err := h.guardCodexThreadSwitch(route, route.threadID); err != nil {
		return renderCodexSwitchFailure(err)
	}
	if err := h.commitCodexSwitchSelection(req, route); err != nil {
		return fmt.Sprintf("切换 Codex 会话失败: 保存当前窗口 Agent: %v", err)
	}
	unlock, err := h.lockCodexSessionThread(req.ctx, route.threadID, "switch")
	if err != nil {
		return renderCodexSwitchControlTimeout(route, err)
	}
	defer unlock()
	started := time.Now()
	resolution, runtimeErr := h.inspectSelectedCodexRuntimeLocked(req.ctx, route, req.agent)
	logCodexSessionControlTimeout("switch", "runtime-inspect", route.threadID, started, runtimeErr)
	if isCodexSessionControlTimeout(runtimeErr) {
		return renderCodexSwitchControlTimeout(route, runtimeErr)
	}
	return h.renderCodexSwitchResult(codexSwitchRenderRequest{
		switchRequest: req, route: route, resolution: resolution, runtimeErr: runtimeErr,
	})
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

func (h *Handler) commitCodexSwitchSelection(req codexSwitchRequest, route codexConversationRoute) error {
	h.bindConversationCwd(req.agent, route.conversationID, route.workspaceRoot)
	h.switchCodexWorkspaceForRoute(
		firstNonBlank(req.options.actorUserID, req.userID), req.userID,
		req.agentName, route.workspaceRoot, req.agent,
	)
	h.ensureCodexSessions().setThread(route.bindingKey, route.workspaceRoot, route.threadID)
	h.setCodexActiveWorkspaceForRoute(route.bindingKey, req.ownerBindingKey, route.workspaceRoot)
	return h.ensureAgentSessions().Set(req.userID, req.agentName)
}

func (h *Handler) renderCodexSwitchResult(render codexSwitchRenderRequest) string {
	req := render.switchRequest
	route := render.route
	resolution := render.resolution
	lines := []string{"已切换会话。", "工作空间: " + shortCodexWorkspaceName(route.workspaceRoot)}
	modelStatus := codexResolutionModelStatus(resolution, h.codexSessionModelStatus(route.threadID))
	lines = append(lines, renderSessionModelStatus(modelStatus)...)
	lines = append(lines, renderCodexOwnerNotice(resolution, route)...)
	if !isCodexSessionControlTimeout(render.runtimeErr) && canObserveCodexTask(resolution, route) {
		state, active, activeErr := h.startExternalCodexTaskIfActive(externalCodexTaskOptions{
			ctx: codexExternalTaskContext(req), actorUserID: firstNonBlank(req.options.actorUserID, req.userID),
			routeUserID: req.userID, agentName: req.agentName, agent: req.agent,
			conversationID: route.conversationID, threadID: route.threadID,
			platform: req.options.platform, accountID: req.options.accountID, reply: req.options.reply,
		})
		if active {
			lines = append(lines, renderExternalCodexActiveNotice(state)...)
		}
		lines = append(lines, renderExternalCodexStateReadError(activeErr)...)
	}
	lines = append(lines, renderCodexRuntimeInspectError(render.runtimeErr)...)
	return wechatCommandText(lines...)
}

func codexExternalTaskContext(req codexSwitchRequest) context.Context {
	if req.options.externalTaskCtx != nil {
		return req.options.externalTaskCtx
	}
	return normalizeContext(req.ctx)
}

// inspectSelectedCodexRuntimeLocked 只读探测已选择会话，失败不回滚用户选择。
func (h *Handler) inspectSelectedCodexRuntimeLocked(ctx context.Context, route codexConversationRoute, ag agent.Agent) (codexRuntimeResolution, error) {
	resolution, err := h.resolveCodexRuntimeLocked(ctx, codexRuntimeResolveOptions{
		route: route, threadID: route.threadID, ag: ag,
	})
	if err == nil {
		return resolution, nil
	}
	intent := h.ensureCodexSessions().controlIntent(route.threadID)
	return codexRuntimeResolution{
		Request: agent.CodexRuntimeRequest{Ref: route.ref(route.threadID), Intent: agentControlIntent(intent)},
		Binding: unknownCodexRuntimeBinding(agent.CodexRuntimeRequest{
			Ref: route.ref(route.threadID), Intent: agentControlIntent(intent),
		}), Live: true,
	}, err
}

// renderCodexRuntimeInspectError 明确说明探测失败，但不把会话选择误报为失败。
func renderCodexRuntimeInspectError(err error) []string {
	if err == nil {
		return nil
	}
	if isCodexSessionControlTimeout(err) {
		return []string{"运行位置探测超时；会话选择已保留。"}
	}
	if isCodexThreadStoreReadError(err) {
		return []string{"运行位置探测失败: 该会话暂时无法读取；会话选择已保留。"}
	}
	return []string{"运行位置探测失败: " + err.Error()}
}

func renderCodexSwitchControlTimeout(route codexConversationRoute, err error) string {
	message := "运行位置探测超时；会话选择已保留。"
	if errors.Is(err, context.Canceled) {
		message = "运行位置探测已取消；会话选择已保留。"
	}
	return wechatCommandText(
		"已切换会话。",
		"工作空间: "+shortCodexWorkspaceName(route.workspaceRoot),
		message,
	)
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
