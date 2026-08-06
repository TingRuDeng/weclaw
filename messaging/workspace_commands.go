package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type workspaceCommandSpec struct {
	Action string
	Target string
}

type workspaceCommandRequest struct {
	Context     context.Context
	ActorUserID string
	RouteUserID string
	AgentName   string
	AgentKind   string
	BindingKey  string
	Platform    platform.PlatformName
	Admin       bool
	Private     bool
	Spec        workspaceCommandSpec
}

func parseWorkspaceCommand(trimmed string, namespace string) (workspaceCommandSpec, bool, error) {
	trimmed = strings.TrimSpace(trimmed)
	rest, ok := strings.CutPrefix(trimmed, namespace)
	if !ok || rest != "" && !isSpaceByte(rest[0]) {
		return workspaceCommandSpec{}, false, nil
	}
	rest = strings.TrimSpace(rest)
	word, rest := cutCommandWord(rest)
	if word != "workspace" {
		return workspaceCommandSpec{}, false, nil
	}
	action, target := cutCommandWord(strings.TrimSpace(rest))
	if action != "add" && action != "remove" {
		return workspaceCommandSpec{}, true, fmt.Errorf(
			"用法: %s workspace add <路径> 或 %s workspace remove <编号|路径>", namespace, namespace,
		)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return workspaceCommandSpec{}, true, fmt.Errorf("工作空间路径不能为空")
	}
	return workspaceCommandSpec{Action: action, Target: target}, true, nil
}

func cutCommandWord(value string) (string, string) {
	for index, r := range value {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return value[:index], value[index:]
		}
	}
	return value, ""
}

func isSpaceByte(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

func (h *Handler) handleWorkspaceCommand(req workspaceCommandRequest) string {
	if !req.Admin {
		return "仅管理员可以登记或移除主机级工作空间。"
	}
	if !req.Private {
		return "工作空间登记只允许在私聊中执行。"
	}
	registry := h.ensureWorkspaceRegistry()
	registry.control.Lock()
	defer registry.control.Unlock()

	if req.Spec.Action == "add" {
		result, err := registry.Add(req.AgentName, req.Spec.Target)
		if err != nil {
			h.auditWorkspaceCommand(req, "failed", "")
			return err.Error()
		}
		h.auditWorkspaceCommand(req, "success", result.Root)
		label := workspaceAgentLabel(req.AgentKind)
		if !result.Changed {
			return fmt.Sprintf("%s 工作空间已登记：%s", label, result.Root)
		}
		return fmt.Sprintf("已登记 %s 工作空间：%s", label, result.Root)
	}

	root, err := h.resolveWorkspaceRemovalTarget(req)
	if err != nil {
		h.auditWorkspaceCommand(req, "failed", "")
		return err.Error()
	}
	if h.workspaceRootInUse(req.AgentKind, root) {
		h.auditWorkspaceCommand(req, "busy", root)
		return "该工作空间仍被窗口使用，请先在所有窗口切换到其他工作空间。"
	}
	result, err := registry.Remove(req.AgentName, root)
	if err != nil {
		h.auditWorkspaceCommand(req, "failed", root)
		return err.Error()
	}
	h.auditWorkspaceCommand(req, "success", result.Root)
	label := workspaceAgentLabel(req.AgentKind)
	if !result.Changed {
		return fmt.Sprintf("%s 工作空间已移除：%s", label, result.Root)
	}
	return fmt.Sprintf("已从 WeClaw 移除 %s 工作空间：%s\n未删除目录或会话历史。", label, result.Root)
}

func (h *Handler) resolveWorkspaceRemovalTarget(req workspaceCommandRequest) (string, error) {
	if index, ok := parseCodexListIndex(req.Spec.Target); ok {
		var groups []codexWorkspaceGroup
		var err error
		if req.AgentKind == "codex" {
			groups, err = h.codexWorkspaceListForAccess(req.BindingKey, true)
		} else {
			var ag agent.Agent
			_, ag, err = h.getClaudeSessionAgent(req.Context)
			if err == nil {
				route := claudeSessionRoute{
					Context: req.Context, ActorUserID: req.ActorUserID, UserID: req.RouteUserID,
					AgentName: req.AgentName, Agent: ag, BindingKey: req.BindingKey, Admin: true,
				}
				groups, err = h.claudeWorkspaceGroupsForRoute(route)
			}
		}
		if err != nil {
			return "", err
		}
		if index < 0 || index >= len(groups) {
			return "", fmt.Errorf("工作空间编号不存在，请先发送 /%s ls 查看。", workspaceAgentNamespace(req.AgentKind))
		}
		return groups[index].Root, nil
	}
	return canonicalWorkspaceRegistryPath(req.Spec.Target, false)
}

func (h *Handler) workspaceRootInUse(agentKind string, root string) bool {
	if agentKind == "claude" {
		return h.ensureClaudeSessions().workspaceInUse(root)
	}
	if h.ensureCodexSessions().workspaceInUse(root) {
		return true
	}
	root, _ = canonicalWorkspaceRegistryPath(root, false)
	h.codexBrowseMu.Lock()
	defer h.codexBrowseMu.Unlock()
	for _, browsing := range h.codexBrowseWorkspaces {
		candidate, _ := canonicalWorkspaceRegistryPath(browsing, false)
		if candidate != "" && candidate == root {
			return true
		}
	}
	return false
}

func (h *Handler) auditWorkspaceCommand(req workspaceCommandRequest, status string, root string) {
	summary := "status=" + status
	if strings.TrimSpace(root) != "" {
		summary += " root=" + root
	}
	h.auditRecord(auditEntry{
		Platform: string(req.Platform), User: req.ActorUserID, Agent: req.AgentName,
		Action: "workspace_" + req.Spec.Action, Summary: summary,
	})
}

func workspaceAgentLabel(agentKind string) string {
	if agentKind == "claude" {
		return "Claude"
	}
	return "Codex"
}

func workspaceAgentNamespace(agentKind string) string {
	if agentKind == "claude" {
		return "cc"
	}
	return "cx"
}

func agentNameFromBindingKey(bindingKey string) string {
	parts := strings.SplitN(bindingKey, "\x00", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func (h *Handler) workspaceRegistrySnapshot(agentName string) workspaceRegistrySnapshot {
	snapshot, err := h.ensureWorkspaceRegistry().Snapshot(agentName)
	if err != nil {
		return workspaceRegistrySnapshot{Hidden: make(map[string]struct{})}
	}
	return snapshot
}

func (h *Handler) lockWorkspaceRegistryControl() func() {
	registry := h.ensureWorkspaceRegistry()
	registry.control.Lock()
	return registry.control.Unlock
}

func (h *Handler) hiddenWorkspaceError(agentName string, workspaceRoot string, namespace string) error {
	if !h.workspaceRegistrySnapshot(agentName).IsHidden(workspaceRoot) {
		return nil
	}
	return fmt.Errorf("当前工作空间已被管理员移除，请发送 /%s ls 重新选择", namespace)
}
