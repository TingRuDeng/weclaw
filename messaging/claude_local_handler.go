package messaging

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

const (
	claudeACPSource     = "acp"
	claudeBindingSource = "binding"
)

// claudeSwitchTargets 从 ACP 目录读取当前用户可访问的全部会话。
func (h *Handler) claudeSwitchTargets(route claudeSessionRoute) ([]codexWorkspaceView, error) {
	catalog, ok := route.Agent.(agent.ClaudeSessionCatalogAgent)
	if !ok {
		return nil, fmt.Errorf("当前 Claude Agent 不支持 session/list")
	}
	sessions, err := catalog.ListClaudeSessions(route.Context)
	if err != nil {
		return nil, fmt.Errorf("读取 Claude 会话目录失败: %w", err)
	}
	views := make([]codexWorkspaceView, 0, len(sessions))
	for _, session := range sessions {
		if !route.Admin && !h.isWorkspaceAllowed(session.Cwd) {
			continue
		}
		views = append(views, claudeSessionView(session))
	}
	return sortClaudeSessionViews(views), nil
}

// claudeDisplayTargets 仅为导航补入当前已绑定但尚未进入 ACP 目录的会话。
// 切换仍使用 claudeSwitchTargets，不能让暂态投影绕过 session/list 校验。
func (h *Handler) claudeDisplayTargets(route claudeSessionRoute) ([]codexWorkspaceView, error) {
	views, err := h.claudeSwitchTargets(route)
	if err != nil {
		return nil, err
	}
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	if binding.Status != claudeBindingReady || strings.TrimSpace(binding.SessionID) == "" {
		return views, nil
	}
	workspaceRoot := normalizeClaudeWorkspaceRoot(binding.WorkspaceRoot)
	if workspaceRoot == "" || !route.Admin && !h.isWorkspaceAllowed(workspaceRoot) {
		return views, nil
	}
	for _, view := range views {
		if view.ThreadID == binding.SessionID {
			return views, nil
		}
	}
	views = append(views, codexWorkspaceView{
		WorkspaceRoot:  workspaceRoot,
		ThreadID:       strings.TrimSpace(binding.SessionID),
		PendingCatalog: true,
		UpdatedAt:      strings.TrimSpace(binding.UpdatedAt),
		Source:         claudeBindingSource,
	})
	return sortClaudeSessionViews(views), nil
}

func sortClaudeSessionViews(views []codexWorkspaceView) []codexWorkspaceView {
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].UpdatedAt != views[j].UpdatedAt {
			return views[i].UpdatedAt > views[j].UpdatedAt
		}
		return views[i].ThreadID < views[j].ThreadID
	})
	return views
}

func claudeSessionView(session agent.ClaudeSession) codexWorkspaceView {
	return codexWorkspaceView{
		WorkspaceRoot: normalizeClaudeWorkspaceRoot(session.Cwd),
		ThreadID:      session.ID,
		ThreadName:    strings.TrimSpace(session.Title),
		UpdatedAt:     strings.TrimSpace(session.UpdatedAt),
		Source:        claudeACPSource,
	}
}

// claudeWorkspaceGroupsForRoute 按工作空间聚合 ACP 目录及当前暂态会话投影。
func (h *Handler) claudeWorkspaceGroupsForRoute(route claudeSessionRoute) ([]codexWorkspaceGroup, error) {
	views, err := h.claudeDisplayTargets(route)
	if err != nil {
		return nil, err
	}
	byRoot := make(map[string]*codexWorkspaceGroup)
	for _, view := range views {
		root := normalizeClaudeWorkspaceRoot(view.WorkspaceRoot)
		if byRoot[root] == nil {
			byRoot[root] = &codexWorkspaceGroup{Name: shortCodexWorkspaceName(root), Root: root}
		}
		byRoot[root].Sessions = append(byRoot[root].Sessions, view)
	}
	return sortedCodexWorkspaceGroups(byRoot), nil
}

// findClaudeWorkspaceGroupForRoute 按卡片 token、手工编号或名称解析可访问工作空间。
func (h *Handler) findClaudeWorkspaceGroupForRoute(route claudeSessionRoute, target string) (codexWorkspaceGroup, error) {
	groups, err := h.claudeWorkspaceGroupsForRoute(route)
	if err != nil {
		return codexWorkspaceGroup{}, err
	}
	target = strings.TrimSpace(target)
	if isFeishuWorkspaceChoiceToken(target) {
		workspaceRoot, ok := h.feishuWorkspaceChoices.consume(
			target, feishuWorkspaceChoiceClaude, route.ActorUserID, route.BindingKey,
		)
		if !ok {
			return codexWorkspaceGroup{}, fmt.Errorf("工作空间卡片已过期，请重新发送 /cc ls。")
		}
		workspaceRoot = normalizeClaudeWorkspaceRoot(workspaceRoot)
		for _, group := range groups {
			if normalizeClaudeWorkspaceRoot(group.Root) == workspaceRoot {
				return group, nil
			}
		}
		return codexWorkspaceGroup{}, fmt.Errorf("工作空间卡片已过期，请重新发送 /cc ls。")
	}
	if index, ok := parseCodexListIndex(target); ok {
		if index < 0 || index >= len(groups) {
			return codexWorkspaceGroup{}, fmt.Errorf("工作空间编号不存在，请先发送 /cc ls 查看。")
		}
		return groups[index], nil
	}
	return findClaudeWorkspaceGroupByName(groups, target)
}

func findClaudeWorkspaceGroupByName(groups []codexWorkspaceGroup, target string) (codexWorkspaceGroup, error) {
	matched := -1
	for index := range groups {
		if groups[index].Name != target {
			continue
		}
		if matched >= 0 {
			return codexWorkspaceGroup{}, fmt.Errorf("工作空间名称 %q 不唯一，请使用编号。", target)
		}
		matched = index
	}
	if matched < 0 {
		return codexWorkspaceGroup{}, fmt.Errorf("工作空间不存在，请先发送 /cc ls 查看。")
	}
	return groups[matched], nil
}

func (h *Handler) claudeSessionsForWorkspace(route claudeSessionRoute, workspaceRoot string) ([]codexWorkspaceView, error) {
	groups, err := h.claudeWorkspaceGroupsForRoute(route)
	if err != nil {
		return nil, err
	}
	workspaceRoot = normalizeClaudeWorkspaceRoot(workspaceRoot)
	for _, group := range groups {
		if group.Root == workspaceRoot {
			return switchableClaudeSessions(group.Sessions), nil
		}
	}
	return nil, nil
}

func switchableClaudeSessions(sessions []codexWorkspaceView) []codexWorkspaceView {
	result := make([]codexWorkspaceView, 0, len(sessions))
	for _, session := range switchableCodexSessions(sessions) {
		if !session.PendingCatalog {
			result = append(result, session)
		}
	}
	return result
}

func claudeWorkspaceGroupHasPendingCatalog(group codexWorkspaceGroup) bool {
	for _, session := range group.Sessions {
		if session.PendingCatalog {
			return true
		}
	}
	return false
}

func claudeWorkspaceGroupLabel(group codexWorkspaceGroup) string {
	if claudeWorkspaceGroupHasPendingCatalog(group) {
		return group.Name + "（当前新会话）"
	}
	return group.Name
}

func (h *Handler) findClaudeSessionForRoute(route claudeSessionRoute, target string) (agent.ClaudeSession, error) {
	views, err := h.claudeSwitchTargets(route)
	if err != nil {
		return agent.ClaudeSession{}, err
	}
	target = strings.TrimSpace(target)
	if index, ok := parseCodexListIndex(target); ok {
		if index < 0 || index >= len(views) {
			return agent.ClaudeSession{}, fmt.Errorf("编号不存在，请先发送 /cc ls 查看可切换会话。")
		}
		return claudeSessionFromView(views[index]), nil
	}
	for _, view := range views {
		if view.ThreadID == target {
			return claudeSessionFromView(view), nil
		}
	}
	return agent.ClaudeSession{}, fmt.Errorf("session 不存在或无权访问，请先发送 /cc ls 查看。")
}

func claudeSessionFromView(view codexWorkspaceView) agent.ClaudeSession {
	return agent.ClaudeSession{ID: view.ThreadID, Cwd: view.WorkspaceRoot, Title: view.ThreadName, UpdatedAt: view.UpdatedAt}
}
