package messaging

import (
	"context"
	"fmt"
	"log"
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
	ctx           context.Context
	taskContext   context.Context
	actorUserID   string
	userID        string
	agentName     string
	workspaceRoot string
	agent         agent.Agent
	platform      platform.PlatformName
	accountID     string
	reply         platform.Replier
}

// handleCodexNewForRoute 创建 thread 后立即绑定当前 frontend。
func (h *Handler) handleCodexNewForRoute(req codexNewRequest) string {
	conversationID := buildCodexConversationID(req.userID, req.agentName, req.workspaceRoot)
	bindingKey := codexBindingKey(req.userID, req.agentName)
	result, err := h.createAndAcquireCodexSessionWithBindingLocked(codexSessionCreateRequest{
		acquire: codexSessionAcquireRequest{
			ctx: req.ctx, taskContext: req.taskContext,
			actorUserID: firstNonBlank(req.actorUserID, req.userID),
			routeUserID: req.userID, agentName: req.agentName, agent: req.agent,
			route: codexConversationRoute{
				bindingKey: bindingKey, workspaceRoot: req.workspaceRoot,
				conversationID: conversationID,
			},
			platform: req.platform, accountID: req.accountID, reply: req.reply,
		},
	})
	if err != nil {
		return renderCodexSessionCreateFailure(result, err)
	}
	return h.renderCodexSessionAcquireResult(
		result.acquireResult, "已创建并绑定。", shortCodexWorkspaceName(req.workspaceRoot),
	)
}

func (h *Handler) handleCodexSwitchForRouteWithOptions(req codexSwitchRequest) string {
	if _, ok := req.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return "当前 Codex Agent 不支持共享 app-server 会话绑定。"
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

// renderCodexSessionAcquireSuccess 只在 frontend binding 已提交后宣告成功。
func (h *Handler) renderCodexSessionAcquireSuccess(result codexSessionAcquireResult) string {
	return h.renderCodexSessionAcquireResult(
		result, "已切换并绑定。", shortCodexWorkspaceName(result.route.workspaceRoot),
	)
}

// renderCodexSessionAcquireResult 展示 frontend binding、共享 host 和观察状态。
func (h *Handler) renderCodexSessionAcquireResult(result codexSessionAcquireResult, headline string, workspaceName string) string {
	lines := []string{headline, "工作空间: " + workspaceName}
	modelStatus := codexResolutionModelStatus(
		result.resolution, h.codexSessionModelStatus(result.route.threadID),
	)
	lines = append(lines, renderCompactSessionModelStatus(modelStatus))
	if result.runtimeErr != nil {
		log.Printf("[codex-session-bind] 绑定已提交但共享 host 暂不可用 thread=%q: %v", result.route.threadID, result.runtimeErr)
		lines = append(lines,
			"运行通道: 暂不可用（窗口绑定已保留）",
			"普通消息暂不会写入；请稍后重试或发送 /cx status 查看状态。",
		)
	}
	if result.externalActive {
		if result.externalProgressCard {
			if result.progressReanchorErr != nil && !result.progressReanchored {
				lines = append(lines, "运行中任务: 任务卡暂时无法移到消息底部；可发送 /ps 查看最新进展。")
			} else if result.progressReanchored {
				lines = append(lines, "运行中任务: 已移到当前消息底部继续更新。")
			} else {
				lines = append(lines, "运行中任务: 进度和结果见下方任务卡。")
				if result.progressReanchorErr != nil {
					lines = append(lines, "旧任务卡停止更新失败；后续以新任务卡为准。")
				}
			}
		} else {
			lines = append(lines, "已开始回传当前任务的进度和结果。")
			lines = append(lines, renderExternalCodexActiveNotice(result.externalState)...)
		}
	}
	if result.agentSessionErr != nil {
		log.Printf("[codex-session-acquire] 保存当前窗口 Agent 失败 thread=%q: %v", result.route.threadID, result.agentSessionErr)
		lines = append(lines, "警告: 保存当前窗口 Agent 失败，请重试。")
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
		view, found, err := h.resolveCodexSessionByIndex(req.bindingKey, index)
		if err != nil {
			return "", "", err
		}
		if found {
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
