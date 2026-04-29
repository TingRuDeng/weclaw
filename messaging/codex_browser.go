package messaging

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexWorkspaceGroup struct {
	Name     string
	Root     string
	Sessions []codexWorkspaceView
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
	groups := h.codexWorkspaceGroups(bindingKey)
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
	return wechatCommandText(lines...)
}

// handleCodexCd 在微信侧进入或退出工作空间浏览层。
func (h *Handler) handleCodexCd(bindingKey string, agentName string, target string, ag agent.Agent) string {
	target = strings.TrimSpace(target)
	if target == ".." {
		h.clearCodexBrowseWorkspace(bindingKey)
		return "已返回工作空间列表。"
	}
	group, err := h.findCodexWorkspaceGroup(bindingKey, target)
	if err != nil {
		return err.Error()
	}
	workspaceRoot := h.switchCodexWorkspace(agentName, group.Root, ag)
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	h.setCodexBrowseWorkspace(bindingKey, workspaceRoot)
	return "工作空间: " + group.Name
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

// codexWorkspaceGroups 按工作空间聚合已记录会话和本机 Codex 会话。
func (h *Handler) codexWorkspaceGroups(bindingKey string) []codexWorkspaceGroup {
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
	sessions := make([]codexWorkspaceView, 0)
	for _, view := range h.codexSwitchTargets(bindingKey) {
		if normalizeCodexWorkspaceRoot(view.WorkspaceRoot) == workspaceRoot {
			sessions = append(sessions, view)
		}
	}
	return sessions
}

func (h *Handler) findCodexWorkspaceGroup(bindingKey string, target string) (codexWorkspaceGroup, error) {
	target = strings.TrimSpace(target)
	groups := h.codexWorkspaceGroups(bindingKey)
	if index, ok := parseCodexListIndex(target); ok {
		if index < 0 || index >= len(groups) {
			return codexWorkspaceGroup{}, fmt.Errorf("工作空间编号不存在，请先发送 /cx ls 查看。")
		}
		return groups[index], nil
	}
	return findCodexWorkspaceGroupByName(groups, target)
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
