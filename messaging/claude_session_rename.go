package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

func (h *Handler) handleClaudeRename(route claudeSessionRoute) string {
	if route.RenameSpec == nil {
		return "用法: /cc rename current|<编号> <名称>"
	}
	renameAgent, ok := route.Agent.(agent.ClaudeSessionRenameAgent)
	if !ok {
		return "当前 Claude Agent 不支持重命名，请更新 WeClaw 和 Claude ACP adapter 后重试。"
	}
	waitCtx, cancel := context.WithTimeout(normalizeContext(route.Context), h.codexSessionLockWaitTimeoutValue())
	defer cancel()
	unlockBinding, err := h.lockAgentExecutionContext(waitCtx, claudeBindingExecutionKey(route.BindingKey))
	if err != nil {
		return "当前 Claude 窗口绑定正在变化，本次重命名未执行。"
	}
	defer unlockBinding()
	selected, err := h.resolveClaudeRenameTarget(route, route.RenameSpec.Target)
	if err != nil {
		return err.Error()
	}

	unlockControl, err := h.lockClaudeSessionControls(claudeSessionLockRequest{
		ctx: route.Context, command: "rename", sessionIDs: []string{selected.ID},
	})
	if err != nil {
		return "当前 Claude 会话控制繁忙，本次重命名未执行。"
	}
	defer unlockControl()
	writerKey := claudeSessionExecutionKey(selected.ID)
	unlockWriter, acquired := h.tryLockAgentExecution(writerKey)
	if !acquired {
		h.auditSessionRename(string(route.Platform), route.ActorUserID, route.AgentName, "claude", selected.ID, "busy")
		return "该 Claude 会话仍有任务运行或状态未确认，本次重命名未执行。"
	}
	defer unlockWriter()
	if _, active := h.activeTask(writerKey); active {
		h.auditSessionRename(string(route.Platform), route.ActorUserID, route.AgentName, "claude", selected.ID, "busy")
		return "该 Claude 会话仍有任务运行或状态未确认，本次重命名未执行。"
	}

	err = renameAgent.RenameClaudeSession(route.Context, selected.ID, route.RenameSpec.Name)
	if err != nil {
		status := "failed"
		switch {
		case errors.Is(err, agent.ErrClaudeRenameOutcomeUnknown):
			status = "unknown"
		case errors.Is(err, agent.ErrClaudeSessionWriterBusy):
			status = "busy"
		}
		h.auditSessionRename(string(route.Platform), route.ActorUserID, route.AgentName, "claude", selected.ID, status)
		switch status {
		case "unknown":
			return "Claude 会话重命名结果暂时无法确认。所有窗口绑定均未改变，请发送 /cc ls 核对会话名称。"
		case "busy":
			return "该 Claude 会话仍有任务运行或状态未确认，本次重命名未执行。"
		}
		if errors.Is(err, agent.ErrClaudeRenameUnsupported) {
			return "当前 Claude ACP adapter 未公布 rename 命令，本次操作未发送给模型；请升级或核对 adapter 后重试。"
		}
		return "Claude 会话重命名失败，所有窗口绑定均未改变：" + sanitizeAgentError(err.Error())
	}
	h.auditSessionRename(string(route.Platform), route.ActorUserID, route.AgentName, "claude", selected.ID, "success")
	return "已重命名 Claude 会话。当前窗口及其他窗口绑定均未改变；发送 /cc ls 查看新名称。"
}

func (h *Handler) resolveClaudeRenameTarget(route claudeSessionRoute, target string) (agent.ClaudeSession, error) {
	target = strings.TrimSpace(target)
	if target == "current" {
		binding, err := h.ensureClaudeSessions().requireWritableBinding(route.BindingKey)
		if err != nil || strings.TrimSpace(binding.SessionID) == "" {
			return agent.ClaudeSession{}, fmt.Errorf("当前窗口没有可写的 Claude 会话，请先发送 /cc ls 选择或 /cc new 新建")
		}
		selected, err := h.findClaudeSessionForRoute(route, binding.SessionID)
		if err != nil {
			return agent.ClaudeSession{}, fmt.Errorf("当前 Claude 会话不存在、已隐藏或无权访问，请发送 /cc ls 重新选择")
		}
		if normalizeClaudeWorkspaceRoot(selected.Cwd) != normalizeClaudeWorkspaceRoot(binding.WorkspaceRoot) {
			return agent.ClaudeSession{}, fmt.Errorf("当前 Claude 会话工作空间与目录不一致，请发送 /cc ls 重新选择")
		}
		return selected, nil
	}
	if _, ok := parseCodexListIndex(target); !ok {
		return agent.ClaudeSession{}, fmt.Errorf("用法: /cc rename current|<编号> <名称>")
	}
	selected, err := h.findClaudeSessionForRoute(route, target)
	if err != nil {
		return agent.ClaudeSession{}, err
	}
	if err := h.hiddenWorkspaceError(route.AgentName, selected.Cwd, "cc"); err != nil {
		return agent.ClaudeSession{}, err
	}
	if !route.Admin && !h.isWorkspaceAllowed(selected.Cwd) && !h.isConfiguredAgentWorkspace(route.AgentName, selected.Cwd) {
		return agent.ClaudeSession{}, fmt.Errorf("该会话工作空间不在允许范围，请发送 /cc ls 重新选择")
	}
	return selected, nil
}
