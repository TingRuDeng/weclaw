package messaging

import (
	"fmt"
	"strings"
)

// SetClaudeLocalSessionDir 设置本机 Claude 数据目录，测试和特殊部署可覆盖默认 ~/.claude。
func (h *Handler) SetClaudeLocalSessionDir(dir string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.claudeLocalSessionDir = strings.TrimSpace(dir)
}

// claudeSwitchTargets 合并 WeClaw 已记录会话与本机 Claude 会话。
func (h *Handler) claudeSwitchTargets(bindingKey string) []codexWorkspaceView {
	localViews, visibleByWorkspace := h.localClaudeSessionSnapshot()
	h.ensureClaudeSessions().clearStaleWorkspaceSessions(bindingKey, visibleByWorkspace)
	storedViews := h.ensureClaudeSessions().listWorkspaces(bindingKey)
	views := make([]codexWorkspaceView, 0, len(storedViews))
	seenSessions := make(map[string]bool, len(storedViews))
	for _, view := range storedViews {
		if !isVisibleClaudeWorkspace(view) {
			continue
		}
		views = append(views, view)
		if view.ThreadID != "" {
			seenSessions[view.ThreadID] = true
		}
	}
	return h.appendLocalClaudeSwitchTargets(views, seenSessions, localViews)
}

func (h *Handler) claudeSwitchTargetsForAccess(bindingKey string, actorUserID string, admin bool) []codexWorkspaceView {
	views := h.claudeSwitchTargets(bindingKey)
	if admin {
		return views
	}
	filtered := make([]codexWorkspaceView, 0, len(views))
	for _, view := range views {
		if h.isWorkspaceAllowed(view.WorkspaceRoot) {
			filtered = append(filtered, view)
		}
	}
	return filtered
}

// claudeWorkspaceGroupsForAccess 按工作空间聚合可访问的 Claude 会话，供文本和卡片导航共用。
func (h *Handler) claudeWorkspaceGroupsForAccess(bindingKey string, actorUserID string, admin bool) []codexWorkspaceGroup {
	byRoot := make(map[string]*codexWorkspaceGroup)
	for _, view := range h.claudeSwitchTargetsForAccess(bindingKey, actorUserID, admin) {
		root := normalizeClaudeWorkspaceRoot(view.WorkspaceRoot)
		if root == "" {
			continue
		}
		if byRoot[root] == nil {
			byRoot[root] = &codexWorkspaceGroup{Name: shortCodexWorkspaceName(root), Root: root}
		}
		byRoot[root].Sessions = append(byRoot[root].Sessions, view)
	}
	return sortedCodexWorkspaceGroups(byRoot)
}

// findClaudeWorkspaceGroupForAccess 按稳定编号或名称解析用户可访问的工作空间。
func (h *Handler) findClaudeWorkspaceGroupForAccess(route claudeSessionRoute, target string) (codexWorkspaceGroup, error) {
	groups := h.claudeWorkspaceGroupsForAccess(route.BindingKey, route.ActorUserID, route.Admin)
	if index, ok := parseCodexListIndex(strings.TrimSpace(target)); ok {
		if index < 0 || index >= len(groups) {
			return codexWorkspaceGroup{}, fmt.Errorf("工作空间编号不存在，请先发送 /cc ls 查看。")
		}
		return groups[index], nil
	}
	return findClaudeWorkspaceGroupByName(groups, strings.TrimSpace(target))
}

// findClaudeWorkspaceGroupByName 使用 Claude 命令文案处理名称缺失和重名。
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

// claudeSessionsForWorkspace 返回指定工作空间内可切换的真实会话。
func (h *Handler) claudeSessionsForWorkspace(route claudeSessionRoute, workspaceRoot string) []codexWorkspaceView {
	workspaceRoot = normalizeClaudeWorkspaceRoot(workspaceRoot)
	for _, group := range h.claudeWorkspaceGroupsForAccess(route.BindingKey, route.ActorUserID, route.Admin) {
		if group.Root == workspaceRoot {
			return switchableCodexSessions(group.Sessions)
		}
	}
	return nil
}

// appendLocalClaudeSwitchTargets 追加未被 WeClaw 记录过的本机会话，避免重复展示同一个 session。
func (h *Handler) appendLocalClaudeSwitchTargets(views []codexWorkspaceView, seenSessions map[string]bool, localViews []codexWorkspaceView) []codexWorkspaceView {
	for _, view := range localViews {
		if view.ThreadID == "" || seenSessions[view.ThreadID] {
			continue
		}
		views = append(views, view)
		seenSessions[view.ThreadID] = true
	}
	return views
}

func isVisibleClaudeWorkspace(view codexWorkspaceView) bool {
	return strings.TrimSpace(view.ThreadID) != "" || view.PendingNewThread
}

func (h *Handler) localClaudeSessions() []codexWorkspaceView {
	h.mu.RLock()
	dir := h.claudeLocalSessionDir
	h.mu.RUnlock()
	return discoverLocalClaudeSessions(dir)
}

func (h *Handler) localClaudeSessionSnapshot() ([]codexWorkspaceView, map[string]map[string]bool) {
	h.mu.RLock()
	dir := h.claudeLocalSessionDir
	h.mu.RUnlock()
	return discoverLocalClaudeSessionSnapshot(dir)
}

func (h *Handler) findLocalClaudeWorkspaceBySession(sessionID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	for _, view := range h.localClaudeSessions() {
		if view.ThreadID == sessionID {
			return view.WorkspaceRoot, true
		}
	}
	return "", false
}
