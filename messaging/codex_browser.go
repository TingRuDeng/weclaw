package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type codexWorkspaceGroup struct {
	Name     string
	Root     string
	Sessions []codexWorkspaceView
}

type codexWorkspaceCdRequest struct {
	Context         context.Context
	TaskContext     context.Context
	UserID          string
	ActorUserID     string
	BindingKey      string
	OwnerBindingKey string
	AgentName       string
	Target          string
	Agent           agent.Agent
	Platform        platform.PlatformName
	AccountID       string
	Reply           platform.Replier
	Admin           bool
}

type codexSingleSessionEntryRequest struct {
	command       codexWorkspaceCdRequest
	workspaceName string
	workspaceRoot string
	session       codexWorkspaceView
}

// codexBrowseWorkspace 返回当前微信用户正在浏览的 Codex 工作空间。
func (h *Handler) codexBrowseWorkspace(bindingKey string) (string, bool) {
	h.codexBrowseMu.Lock()
	defer h.codexBrowseMu.Unlock()
	root := normalizeCodexWorkspaceRoot(h.codexBrowseWorkspaces[bindingKey])
	return root, root != ""
}

// setCodexBrowseWorkspace 记录当前微信用户进入的 Codex 工作空间层。
func (h *Handler) setCodexBrowseWorkspace(bindingKey string, workspaceRoot string) {
	h.codexBrowseMu.Lock()
	defer h.codexBrowseMu.Unlock()
	if h.codexBrowseWorkspaces == nil {
		h.codexBrowseWorkspaces = make(map[string]string)
	}
	h.codexBrowseWorkspaces[bindingKey] = normalizeCodexWorkspaceRoot(workspaceRoot)
}

// clearCodexBrowseWorkspace 返回工作空间列表层，不改变 Codex Agent 的真实 cwd。
func (h *Handler) clearCodexBrowseWorkspace(bindingKey string) {
	h.codexBrowseMu.Lock()
	defer h.codexBrowseMu.Unlock()
	delete(h.codexBrowseWorkspaces, bindingKey)
}

func (h *Handler) renderCodexWorkspaceListForAccess(bindingKey string, actorUserID string, admin bool) string {
	groups := h.codexWorkspaceGroupsForAccess(bindingKey, actorUserID, admin)
	if len(groups) == 0 {
		return "当前还没有 Codex 工作空间。"
	}
	lines := []string{"Codex 工作空间:"}
	for index, group := range groups {
		lines = append(lines, fmt.Sprintf("%d. %s", index, group.Name))
	}
	return wechatCommandText(lines...)
}

// renderCodexSessionList 只展示当前工作空间内的会话名称，thread id 仅内部使用。
func (h *Handler) renderCodexSessionList(bindingKey string, workspaceRoot string) string {
	sessions := h.codexSessionsForWorkspace(bindingKey, workspaceRoot)
	if len(sessions) == 0 {
		return wechatCommandText(shortCodexWorkspaceName(workspaceRoot)+" 会话", "当前工作空间还没有可切换会话。")
	}
	lines := []string{shortCodexWorkspaceName(workspaceRoot) + " 会话"}
	for index, session := range sessions {
		lines = append(lines, fmt.Sprintf("%d. %s", index, codexSessionDisplayName(session)))
	}
	lines = append(lines, "", "发送 /cx cd .. 返回工作空间列表。")
	return wechatCommandText(lines...)
}

// handleCodexCdResult 返回工作空间导航文本及卡片展示状态。
func (h *Handler) handleCodexCdResult(req codexWorkspaceCdRequest) navigationCommandResult {
	req.Target = strings.TrimSpace(req.Target)
	if req.Target == ".." {
		h.clearCodexBrowseWorkspace(req.BindingKey)
		return cardNavigationResult(wechatCommandText("已返回工作空间列表。", h.renderCodexWorkspaceListForAccess(req.BindingKey, req.ActorUserID, req.Admin)))
	}
	group, err := h.findCodexWorkspaceGroupForAccess(req.BindingKey, req.ActorUserID, req.Admin, req.Target)
	if err != nil {
		return textNavigationResult(err.Error())
	}
	workspaceRoot := h.switchCodexWorkspaceForRoute(req.ActorUserID, req.UserID, req.AgentName, group.Root, req.Agent)
	h.setCodexActiveWorkspaceForRoute(req.BindingKey, req.OwnerBindingKey, workspaceRoot)
	h.setCodexBrowseWorkspace(req.BindingKey, workspaceRoot)
	return h.enterCodexWorkspace(req, group, workspaceRoot)
}

// setCodexActiveWorkspaceForRoute 只更新当前 route，避免平台路由会话互相污染工作空间。
func (h *Handler) setCodexActiveWorkspaceForRoute(bindingKey string, _ string, workspaceRoot string) {
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
}

// enterCodexWorkspace 根据会话数量决定自动切换、创建草稿或展示列表。
func (h *Handler) enterCodexWorkspace(req codexWorkspaceCdRequest, group codexWorkspaceGroup, workspaceRoot string) navigationCommandResult {
	sessions := switchableCodexSessions(group.Sessions)
	if len(sessions) == 0 {
		return h.enterCodexWorkspaceWithoutSessionsResult(req, group.Name, workspaceRoot)
	}
	if len(sessions) == 1 {
		return h.enterCodexWorkspaceWithSingleSessionResult(codexSingleSessionEntryRequest{
			command: req, workspaceName: group.Name, workspaceRoot: workspaceRoot, session: sessions[0],
		})
	}
	return cardNavigationResult(wechatCommandText("工作空间: "+group.Name, h.renderCodexSessionList(req.BindingKey, workspaceRoot)))
}

// enterCodexWorkspaceWithoutSessionsResult 返回未绑定工作空间的导航结果。
func (h *Handler) enterCodexWorkspaceWithoutSessionsResult(req codexWorkspaceCdRequest, workspaceName string, workspaceRoot string) navigationCommandResult {
	h.ensureCodexSessions().ensureWorkspace(req.BindingKey, workspaceRoot)
	return cardNavigationResult(wechatCommandText(
		"当前工作空间没有可用会话。",
		"工作空间: "+workspaceName,
		"发送 /cx new 创建新会话。",
	))
}

// enterCodexWorkspaceWithSingleSessionResult 返回自动切换唯一会话后的导航结果。
func (h *Handler) enterCodexWorkspaceWithSingleSessionResult(entry codexSingleSessionEntryRequest) navigationCommandResult {
	req := entry.command
	workspaceName, workspaceRoot, session := entry.workspaceName, entry.workspaceRoot, entry.session
	_, ok := req.Agent.(agent.CodexThreadAgent)
	if !ok {
		return cardNavigationResult(wechatCommandText("工作空间: "+workspaceName, h.renderCodexSessionList(req.BindingKey, workspaceRoot)))
	}
	conversationID := buildCodexConversationID(req.UserID, req.AgentName, workspaceRoot)
	h.bindConversationCwd(req.Agent, conversationID, workspaceRoot)
	route := codexConversationRoute{
		bindingKey: req.BindingKey, workspaceRoot: workspaceRoot,
		conversationID: conversationID, threadID: session.ThreadID,
	}
	h.ensureCodexSessions().setThread(req.BindingKey, workspaceRoot, session.ThreadID)
	unlock, err := h.lockCodexSessionThread(req.Context, session.ThreadID, "cd")
	if err != nil {
		return cardNavigationResult(wechatCommandText(
			"已进入工作空间并切换会话。",
			"工作空间: "+workspaceName,
			"运行位置探测超时；会话选择已保留。",
		))
	}
	defer unlock()
	started := time.Now()
	resolution, runtimeErr := h.inspectSelectedCodexRuntimeLocked(req.Context, route, req.Agent)
	logCodexSessionControlTimeout("cd", "runtime-inspect", session.ThreadID, started, runtimeErr)
	lines := []string{"已进入工作空间并切换会话。", "工作空间: " + workspaceName}
	modelStatus := codexResolutionModelStatus(resolution, h.codexSessionModelStatus(session.ThreadID))
	lines = append(lines, renderSessionModelStatus(modelStatus)...)
	lines = append(lines, renderCodexOwnerNotice(resolution, route)...)
	if !isCodexSessionControlTimeout(runtimeErr) && canObserveCodexTask(resolution, route) {
		state, active, activeErr := h.startExternalCodexTaskIfActive(externalCodexTaskOptions{
			ctx:            firstCodexContext(req.TaskContext, req.Context),
			actorUserID:    firstNonBlank(req.ActorUserID, req.UserID),
			routeUserID:    req.UserID,
			agentName:      req.AgentName,
			agent:          req.Agent,
			conversationID: conversationID,
			threadID:       session.ThreadID,
			platform:       req.Platform,
			accountID:      req.AccountID,
			reply:          req.Reply,
		})
		if active {
			lines = append(lines, renderExternalCodexActiveNotice(state)...)
		}
		lines = append(lines, renderExternalCodexStateReadError(activeErr)...)
	}
	lines = append(lines, renderCodexRuntimeInspectError(runtimeErr)...)
	return cardNavigationResult(wechatCommandText(lines...))
}

func firstCodexContext(primary context.Context, fallback context.Context) context.Context {
	if primary != nil {
		return primary
	}
	return normalizeContext(fallback)
}

// renderCodexPwd 显示当前浏览层级，帮助用户确认 /cx ls 会列什么。
func (h *Handler) renderCodexPwd(bindingKey string) string {
	workspaceRoot, ok := h.codexBrowseWorkspace(bindingKey)
	if !ok {
		return wechatCommandText("浏览层级: 工作空间", "发送 /cx ls 查看工作空间。")
	}
	return wechatCommandText(
		"浏览层级: 会话",
		"工作空间: "+shortCodexWorkspaceName(workspaceRoot),
		"路径: "+workspaceRoot,
	)
}
