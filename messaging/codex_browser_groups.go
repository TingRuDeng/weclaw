package messaging

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// codexWorkspaceGroups 按 Codex App 项目列表聚合会话，缺少 App 状态时回退历史会话聚合。
func (h *Handler) codexWorkspaceGroups(bindingKey string) []codexWorkspaceGroup {
	if workspaces := h.codexAppWorkspaces(); len(workspaces) > 0 {
		return h.codexWorkspaceGroupsForAppWorkspaces(bindingKey, workspaces)
	}
	byRoot := map[string]*codexWorkspaceGroup{}
	for _, view := range h.codexSwitchTargets(bindingKey) {
		root := normalizeCodexWorkspaceRoot(view.WorkspaceRoot)
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

func (h *Handler) codexWorkspaceGroupsForUser(bindingKey string, actorUserID string) []codexWorkspaceGroup {
	return h.codexWorkspaceGroupsForAccess(bindingKey, actorUserID, h.isAdminUser(actorUserID))
}

func (h *Handler) codexWorkspaceGroupsForAccess(bindingKey string, actorUserID string, admin bool) []codexWorkspaceGroup {
	return h.filterCodexWorkspaceGroupsForAccess(h.codexWorkspaceGroups(bindingKey), admin)
}

// codexWorkspaceListForAccess 只读取顶层项目，不为 /cx ls 预加载每个项目的会话。
func (h *Handler) codexWorkspaceListForAccess(bindingKey string, admin bool) []codexWorkspaceGroup {
	var groups []codexWorkspaceGroup
	if workspaces := h.codexAppWorkspaces(); len(workspaces) > 0 {
		groups = codexWorkspaceHeadersForAppWorkspaces(workspaces)
	} else {
		groups = h.codexWorkspaceGroups(bindingKey)
	}
	return h.filterCodexWorkspaceGroupsForAccess(groups, admin)
}

func (h *Handler) filterCodexWorkspaceGroupsForAccess(groups []codexWorkspaceGroup, admin bool) []codexWorkspaceGroup {
	if admin {
		return groups
	}
	filtered := make([]codexWorkspaceGroup, 0, len(groups))
	for _, group := range groups {
		if h.isWorkspaceAllowed(group.Root) || h.isConfiguredWorkspace(group.Root) {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func (h *Handler) codexWorkspaceGroupsForAppWorkspaces(bindingKey string, workspaces []codexAppWorkspace) []codexWorkspaceGroup {
	groups := codexWorkspaceHeadersForAppWorkspaces(workspaces)
	for index := range groups {
		groups[index].Sessions = h.codexSessionsForWorkspace(bindingKey, groups[index].Root)
	}
	return groups
}

func codexWorkspaceHeadersForAppWorkspaces(workspaces []codexAppWorkspace) []codexWorkspaceGroup {
	seenRoots := make(map[string]bool, len(workspaces))
	groups := make([]codexWorkspaceGroup, 0, len(workspaces))
	for _, workspace := range workspaces {
		root := normalizeCodexWorkspaceRoot(workspace.Root)
		if root == "" || seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		name := strings.TrimSpace(workspace.Name)
		if name == "" {
			name = shortCodexWorkspaceName(root)
		}
		groups = append(groups, codexWorkspaceGroup{Name: name, Root: root})
	}
	return groups
}

func sortedCodexWorkspaceGroups(byRoot map[string]*codexWorkspaceGroup) []codexWorkspaceGroup {
	groups := make([]codexWorkspaceGroup, 0, len(byRoot))
	for _, group := range byRoot {
		groups = append(groups, *group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Name != groups[j].Name {
			return groups[i].Name < groups[j].Name
		}
		return groups[i].Root < groups[j].Root
	})
	return groups
}

// codexSessionsForWorkspace 返回当前工作空间内可切换的会话，保持 /cx ls 与 /cx switch 编号一致。
func (h *Handler) codexSessionsForWorkspace(bindingKey string, workspaceRoot string) []codexWorkspaceView {
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	if sessions := h.codexAppWorkspaceThreads(workspaceRoot); sessions != nil {
		h.ensureCodexSessions().clearStaleWorkspaceThread(bindingKey, workspaceRoot, codexVisibleThreadSet(sessions))
		return sessions
	}
	sessions := make([]codexWorkspaceView, 0)
	for _, view := range h.codexSwitchTargets(bindingKey) {
		if normalizeCodexWorkspaceRoot(view.WorkspaceRoot) == workspaceRoot {
			sessions = append(sessions, view)
		}
	}
	return sessions
}

func codexVisibleThreadSet(sessions []codexWorkspaceView) map[string]bool {
	visible := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		threadID := strings.TrimSpace(session.ThreadID)
		if threadID != "" {
			visible[threadID] = true
		}
	}
	return visible
}

func switchableCodexSessions(sessions []codexWorkspaceView) []codexWorkspaceView {
	result := make([]codexWorkspaceView, 0, len(sessions))
	for _, session := range sessions {
		if strings.TrimSpace(session.ThreadID) == "" || session.PendingNewThread {
			continue
		}
		result = append(result, session)
	}
	return result
}

func (h *Handler) findCodexWorkspaceGroupForAccess(bindingKey string, actorUserID string, admin bool, target string) (codexWorkspaceGroup, error) {
	target = strings.TrimSpace(target)
	groups := h.codexWorkspaceListForAccess(bindingKey, admin)
	var group codexWorkspaceGroup
	if isFeishuWorkspaceChoiceToken(target) {
		workspaceRoot, ok := h.feishuWorkspaceChoices.consume(
			target, feishuWorkspaceChoiceCodex, actorUserID, bindingKey,
		)
		if !ok {
			return codexWorkspaceGroup{}, fmt.Errorf("工作空间卡片已过期，请重新发送 /cx ls。")
		}
		workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
		found := false
		for _, candidate := range groups {
			if normalizeCodexWorkspaceRoot(candidate.Root) == workspaceRoot {
				group = candidate
				found = true
				break
			}
		}
		if !found {
			return codexWorkspaceGroup{}, fmt.Errorf("工作空间卡片已过期，请重新发送 /cx ls。")
		}
	} else if index, ok := parseCodexListIndex(target); ok {
		if index < 0 || index >= len(groups) {
			return codexWorkspaceGroup{}, fmt.Errorf("工作空间编号不存在，请先发送 /cx ls 查看。")
		}
		group = groups[index]
	} else {
		matched, err := findCodexWorkspaceGroupByName(groups, target)
		if err != nil {
			return codexWorkspaceGroup{}, err
		}
		group = matched
	}
	group.Sessions = h.codexSessionsForWorkspace(bindingKey, group.Root)
	return group, nil
}

func findCodexWorkspaceGroupByName(groups []codexWorkspaceGroup, target string) (codexWorkspaceGroup, error) {
	var matched *codexWorkspaceGroup
	for index := range groups {
		if groups[index].Name != target {
			continue
		}
		if matched != nil {
			return codexWorkspaceGroup{}, fmt.Errorf("工作空间名称 %q 不唯一，请使用编号。", target)
		}
		matched = &groups[index]
	}
	if matched == nil {
		return codexWorkspaceGroup{}, fmt.Errorf("工作空间不存在，请先发送 /cx ls 查看。")
	}
	return *matched, nil
}

// resolveCodexSessionByIndex 在当前工作空间内解析会话编号。
func (h *Handler) resolveCodexSessionByIndex(bindingKey string, index int) (codexWorkspaceView, bool) {
	workspaceRoot, ok := h.codexBrowseWorkspace(bindingKey)
	if !ok {
		return codexWorkspaceView{}, false
	}
	sessions := h.codexSessionsForWorkspace(bindingKey, workspaceRoot)
	if index < 0 || index >= len(sessions) {
		return codexWorkspaceView{}, false
	}
	return sessions[index], true
}

func shortCodexWorkspaceName(workspaceRoot string) string {
	name := filepath.Base(normalizeCodexWorkspaceRoot(workspaceRoot))
	if name == "." || name == string(filepath.Separator) {
		return normalizeCodexWorkspaceRoot(workspaceRoot)
	}
	return name
}

func codexSessionDisplayName(view codexWorkspaceView) string {
	if view.PendingNewThread {
		return "新会话草稿"
	}
	if name := strings.TrimSpace(view.ThreadName); name != "" {
		return name
	}
	return "未命名会话"
}

func (h *Handler) codexSessionLabelForStatus(bindingKey string, workspaceRoot string, threadID string, pending bool) string {
	threadID = strings.TrimSpace(threadID)
	if pending {
		return "新会话草稿"
	}
	if threadID == "" {
		return "未绑定"
	}
	if session, ok := h.findLocalCodexSessionByThread(threadID); ok {
		return codexSessionDisplayName(session)
	}
	for _, session := range h.codexSessionsForWorkspace(bindingKey, workspaceRoot) {
		if strings.TrimSpace(session.ThreadID) == threadID {
			return codexSessionDisplayName(session)
		}
	}
	return "未命名会话"
}
