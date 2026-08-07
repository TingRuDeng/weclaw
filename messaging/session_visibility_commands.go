package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type sessionVisibilityCommandSpec struct {
	Action string
	Target string
}

type sessionVisibilityCommandRequest struct {
	Context     context.Context
	ActorUserID string
	RouteUserID string
	AgentName   string
	AgentKind   string
	BindingKey  string
	Agent       agent.Agent
	Platform    platform.PlatformName
	Admin       bool
	Private     bool
	Spec        sessionVisibilityCommandSpec
}

func parseSessionVisibilityCommand(trimmed string, namespace string) (sessionVisibilityCommandSpec, bool, error) {
	trimmed = strings.TrimSpace(trimmed)
	rest, ok := strings.CutPrefix(trimmed, namespace)
	if !ok || rest != "" && !isSpaceByte(rest[0]) {
		return sessionVisibilityCommandSpec{}, false, nil
	}
	command, rest := cutCommandWord(strings.TrimSpace(rest))
	if command != "session" {
		return sessionVisibilityCommandSpec{}, false, nil
	}
	action, target := cutCommandWord(strings.TrimSpace(rest))
	target = strings.TrimSpace(target)
	if (action != "remove" && action != "restore") || target == "" || strings.ContainsAny(target, " \t\r\n") {
		return sessionVisibilityCommandSpec{}, true, fmt.Errorf(
			"用法: %s session remove <编号|会话ID> 或 %s session restore <会话ID>", namespace, namespace,
		)
	}
	if action == "restore" {
		if _, numeric := parseCodexListIndex(target); numeric {
			return sessionVisibilityCommandSpec{}, true, fmt.Errorf("恢复会话必须使用稳定会话 ID，不能使用编号")
		}
	}
	return sessionVisibilityCommandSpec{Action: action, Target: target}, true, nil
}

func (h *Handler) handleSessionVisibilityCommand(req sessionVisibilityCommandRequest) string {
	if !req.Admin {
		return "仅管理员可以隐藏或恢复主机级会话导航。"
	}
	if !req.Private {
		return "会话隐藏与恢复只允许在私聊中执行。"
	}
	registry := h.ensureWorkspaceRegistry()
	registry.control.Lock()
	defer registry.control.Unlock()

	if req.Spec.Action == "restore" {
		result, err := registry.RestoreSession(req.AgentName, req.Spec.Target)
		if err != nil {
			h.auditSessionVisibility(req, "failed", req.Spec.Target)
			return err.Error()
		}
		h.auditSessionVisibility(req, "success", result.SessionID)
		label := workspaceAgentLabel(req.AgentKind)
		if !result.Changed {
			return fmt.Sprintf("%s 会话已处于可见状态：%s", label, result.SessionID)
		}
		return fmt.Sprintf("已恢复 %s 会话：%s\n发送 /%s ls 可重新选择。", label, result.SessionID, workspaceAgentNamespace(req.AgentKind))
	}

	sessionID, err := h.resolveSessionVisibilityTarget(req)
	if err != nil {
		h.auditSessionVisibility(req, "failed", req.Spec.Target)
		return err.Error()
	}
	if err := h.ensureSessionCanBeHidden(req, sessionID); err != nil {
		h.auditSessionVisibility(req, "busy", sessionID)
		return err.Error()
	}
	result, err := registry.HideSession(req.AgentName, sessionID)
	if err != nil {
		h.auditSessionVisibility(req, "failed", sessionID)
		return err.Error()
	}
	h.auditSessionVisibility(req, "success", result.SessionID)
	label := workspaceAgentLabel(req.AgentKind)
	restore := fmt.Sprintf("/%s session restore %s", workspaceAgentNamespace(req.AgentKind), result.SessionID)
	if !result.Changed {
		return fmt.Sprintf("%s 会话已从 WeClaw 导航隐藏。\n恢复命令：%s", label, restore)
	}
	return fmt.Sprintf("已从 WeClaw 导航隐藏 %s 会话。\n未删除 Agent 会话或历史。\n恢复命令：%s", label, restore)
}

func (h *Handler) resolveSessionVisibilityTarget(req sessionVisibilityCommandRequest) (string, error) {
	target := strings.TrimSpace(req.Spec.Target)
	snapshot, err := h.ensureWorkspaceRegistry().Snapshot(req.AgentName)
	if err != nil {
		return "", err
	}
	if _, numeric := parseCodexListIndex(target); !numeric && snapshot.IsSessionHidden(target) {
		return target, nil
	}
	if req.AgentKind == "claude" {
		route := claudeSessionRoute{
			Context: req.Context, ActorUserID: req.ActorUserID, UserID: req.RouteUserID,
			AgentName: req.AgentName, Agent: req.Agent, BindingKey: req.BindingKey, Admin: true,
		}
		selected, err := h.findClaudeSessionForRoute(route, target)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(selected.ID), nil
	}
	if index, numeric := parseCodexListIndex(target); numeric {
		if index < 0 {
			return "", fmt.Errorf("会话编号不存在，请先发送 /cx ls 查看")
		}
		if _, browsing := h.codexBrowseWorkspace(req.BindingKey); browsing {
			view, found, err := h.resolveCodexSessionByIndex(req.BindingKey, index)
			if err != nil {
				return "", err
			}
			if !found || strings.TrimSpace(view.ThreadID) == "" || view.PendingNewThread {
				return "", fmt.Errorf("会话编号不存在，请先发送 /cx ls 查看当前工作空间会话")
			}
			return strings.TrimSpace(view.ThreadID), nil
		}
		views := h.codexSwitchTargets(req.BindingKey)
		if index >= len(views) || strings.TrimSpace(views[index].ThreadID) == "" || views[index].PendingNewThread {
			return "", fmt.Errorf("会话编号不存在，请先发送 /cx ls 查看")
		}
		return strings.TrimSpace(views[index].ThreadID), nil
	}
	for _, view := range h.codexSwitchTargets(req.BindingKey) {
		if strings.TrimSpace(view.ThreadID) == target {
			return target, nil
		}
	}
	return "", fmt.Errorf("Codex 会话不存在或已不可见，请先发送 /cx ls 查看")
}

func (h *Handler) ensureSessionCanBeHidden(req sessionVisibilityCommandRequest, sessionID string) error {
	if req.AgentKind == "claude" {
		unlock, err := h.lockClaudeSessionControls(claudeSessionLockRequest{
			ctx: req.Context, command: "session remove", sessionIDs: []string{sessionID},
		})
		if err != nil {
			return fmt.Errorf("该 Claude 会话控制繁忙，本次隐藏未执行")
		}
		defer unlock()
		if len(h.ensureClaudeSessions().sessionBindingKeys(sessionID)) > 0 {
			return fmt.Errorf("该 Claude 会话仍被窗口绑定，请先在所有窗口切换或新建其他会话")
		}
		if h.hasNonterminalSessionTask(claudeSessionExecutionKey(sessionID)) {
			return fmt.Errorf("该 Claude 会话仍有任务运行或状态未确认，请等待任务结束后重试")
		}
		return nil
	}
	unlock, err := h.lockCodexSessionThread(req.Context, sessionID, "session remove")
	if err != nil {
		return fmt.Errorf("该 Codex 会话控制繁忙，本次隐藏未执行")
	}
	defer unlock()
	if len(h.ensureCodexSessions().remoteThreadBindingKeys(sessionID)) > 0 {
		return fmt.Errorf("该 Codex 会话仍被窗口绑定，请先在所有窗口切换或新建其他会话")
	}
	if h.hasNonterminalCodexTaskForThread(sessionID) {
		return fmt.Errorf("该 Codex 会话仍有任务运行或状态未确认，请等待任务结束后重试")
	}
	return nil
}

func (h *Handler) hasNonterminalSessionTask(taskKey string) bool {
	task, active := h.activeTask(taskKey)
	if !active || task == nil {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.phase != codexTaskTerminal
}

func (h *Handler) auditSessionVisibility(req sessionVisibilityCommandRequest, status string, sessionID string) {
	h.auditRecord(auditEntry{
		Platform: string(req.Platform), User: req.ActorUserID, Agent: req.AgentName,
		Action:  req.AgentKind + "_session_" + req.Spec.Action,
		Summary: "status=" + status + " session=" + redactedSessionIdentifier(sessionID),
	})
}
