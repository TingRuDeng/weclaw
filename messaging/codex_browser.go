package messaging

import (
	"context"
	"fmt"
	"strings"

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

// renderCodexWorkspaceList 只展示工作空间短名称，避免微信里刷出长路径和 thread id。
func (h *Handler) renderCodexWorkspaceList(bindingKey string) string {
	return h.renderCodexWorkspaceListForAccess(bindingKey, "", false)
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

// handleCodexCd 在微信侧进入或退出工作空间浏览层。
func (h *Handler) handleCodexCd(req codexWorkspaceCdRequest) string {
	req.Target = strings.TrimSpace(req.Target)
	if req.Target == ".." {
		h.clearCodexBrowseWorkspace(req.BindingKey)
		return wechatCommandText("已返回工作空间列表。", h.renderCodexWorkspaceListForAccess(req.BindingKey, req.ActorUserID, req.Admin))
	}
	group, err := h.findCodexWorkspaceGroupForAccess(req.BindingKey, req.ActorUserID, req.Admin, req.Target)
	if err != nil {
		return err.Error()
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
func (h *Handler) enterCodexWorkspace(req codexWorkspaceCdRequest, group codexWorkspaceGroup, workspaceRoot string) string {
	sessions := switchableCodexSessions(group.Sessions)
	if len(sessions) == 0 {
		return h.enterCodexWorkspaceWithNewDraft(req, group.Name, workspaceRoot)
	}
	if len(sessions) == 1 {
		return h.enterCodexWorkspaceWithSingleSession(req, group.Name, workspaceRoot, sessions[0])
	}
	return wechatCommandText("工作空间: "+group.Name, h.renderCodexSessionList(req.BindingKey, workspaceRoot))
}

// enterCodexWorkspaceWithNewDraft 清理当前 thread，下一条 Codex 普通任务会创建新会话。
func (h *Handler) enterCodexWorkspaceWithNewDraft(req codexWorkspaceCdRequest, workspaceName string, workspaceRoot string) string {
	if codexAg, ok := req.Agent.(agent.CodexThreadAgent); ok {
		conversationID := buildCodexConversationID(req.UserID, req.AgentName, workspaceRoot)
		h.bindConversationCwd(req.Agent, conversationID, workspaceRoot)
		codexAg.ClearCodexThread(conversationID)
	}
	h.ensureCodexSessions().setPendingNew(req.BindingKey, workspaceRoot)
	return wechatCommandText("已进入工作空间并创建新会话草稿。", "工作空间: "+workspaceName)
}

// enterCodexWorkspaceWithSingleSession 自动切换唯一会话；坏历史无法恢复时改为新会话草稿。
func (h *Handler) enterCodexWorkspaceWithSingleSession(req codexWorkspaceCdRequest, workspaceName string, workspaceRoot string, session codexWorkspaceView) string {
	codexAg, ok := req.Agent.(agent.CodexThreadAgent)
	if !ok {
		return wechatCommandText("工作空间: "+workspaceName, h.renderCodexSessionList(req.BindingKey, workspaceRoot))
	}
	conversationID := buildCodexConversationID(req.UserID, req.AgentName, workspaceRoot)
	h.bindConversationCwd(req.Agent, conversationID, workspaceRoot)
	if err := codexAg.UseCodexThread(req.Context, conversationID, session.ThreadID); err != nil {
		if isCodexThreadStoreReadError(err) {
			codexAg.ClearCodexThread(conversationID)
			h.ensureCodexSessions().setPendingNew(req.BindingKey, workspaceRoot)
			return wechatCommandText(
				"已进入工作空间并创建新会话草稿。",
				"工作空间: "+workspaceName,
				"原会话无法被微信接手，已改用新会话。",
			)
		}
		return fmt.Sprintf("切换线程失败: %v", err)
	}
	h.ensureCodexSessions().setThread(req.BindingKey, workspaceRoot, session.ThreadID)
	lines := []string{"已进入工作空间并切换会话。", "工作空间: " + workspaceName}
	lines = append(lines, renderSessionModelStatus(h.codexSessionModelStatus(session.ThreadID))...)
	state, active, activeErr := h.startExternalCodexTaskIfActive(externalCodexTaskOptions{
		ctx:            req.Context,
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
	return wechatCommandText(lines...)
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
