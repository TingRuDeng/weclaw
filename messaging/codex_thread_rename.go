package messaging

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexThreadRenameTarget struct {
	workspaceRoot string
	threadID      string
}

func (h *Handler) handleCodexRenameCommand(runtime codexSessionCommandRuntime) string {
	if runtime.renameSpec == nil {
		return "用法: /cx rename current|<编号> <名称>"
	}
	renameAgent, ok := runtime.agent.(agent.CodexThreadRenameAgent)
	if !ok {
		return "当前 Codex Agent 不支持重命名，请更新 WeClaw 和 Codex CLI 后重试。"
	}
	target, err := h.resolveCodexRenameTarget(runtime, runtime.renameSpec.Target)
	if err != nil {
		return err.Error()
	}

	unlockThread, err := h.lockCodexSessionThread(runtime.ctx, target.threadID, "rename")
	if err != nil {
		return "当前 Codex 会话控制繁忙，本次重命名未执行。"
	}
	defer unlockThread()
	if h.hasNonterminalCodexTaskForThread(target.threadID) {
		h.auditSessionRename(string(runtime.req.Platform), runtime.actorUserID, runtime.agentName, "codex", target.threadID, "busy")
		return "该 Codex 会话仍有任务运行或状态未确认，请等待任务结束并用 /cx status 核对后再重命名。"
	}

	err = renameAgent.RenameCodexThread(runtime.ctx, target.threadID, runtime.renameSpec.Name)
	if err != nil {
		status := "failed"
		if errors.Is(err, agent.ErrCodexRenameOutcomeUnknown) {
			status = "unknown"
		}
		h.auditSessionRename(string(runtime.req.Platform), runtime.actorUserID, runtime.agentName, "codex", target.threadID, status)
		if status == "unknown" {
			return "Codex 会话重命名结果暂时无法确认。当前窗口绑定未改变，请发送 /cx ls 核对会话名称。"
		}
		return "Codex 会话重命名失败，当前窗口绑定未改变：" + sanitizeAgentError(err.Error())
	}
	h.auditSessionRename(string(runtime.req.Platform), runtime.actorUserID, runtime.agentName, "codex", target.threadID, "success")
	return "已重命名 Codex 会话。当前窗口及其他窗口绑定均未改变；发送 /cx ls 查看新名称。"
}

func (h *Handler) resolveCodexRenameTarget(runtime codexSessionCommandRuntime, value string) (codexThreadRenameTarget, error) {
	value = strings.TrimSpace(value)
	if value == "current" {
		threadID, pending := h.ensureCodexSessions().getThread(runtime.bindingKey, runtime.workspaceRoot)
		threadID = strings.TrimSpace(threadID)
		switch {
		case pending:
			return codexThreadRenameTarget{}, fmt.Errorf("当前是尚未创建的 Codex 新会话草稿，不能重命名")
		case threadID == "":
			return codexThreadRenameTarget{}, fmt.Errorf("当前窗口尚未绑定 Codex 会话，请先发送 /cx ls 选择")
		}
		if err := h.validateCodexRenameWorkspace(runtime, runtime.workspaceRoot); err != nil {
			return codexThreadRenameTarget{}, err
		}
		return codexThreadRenameTarget{workspaceRoot: runtime.workspaceRoot, threadID: threadID}, nil
	}

	index, ok := parseCodexListIndex(value)
	if !ok {
		return codexThreadRenameTarget{}, fmt.Errorf("用法: /cx rename current|<编号> <名称>")
	}
	workspaceRoot, browsing := h.codexBrowseWorkspace(runtime.bindingKey)
	if !browsing {
		return codexThreadRenameTarget{}, fmt.Errorf("请先发送 /cx ls 并进入工作空间，再按会话编号重命名")
	}
	if err := h.validateCodexRenameWorkspace(runtime, workspaceRoot); err != nil {
		return codexThreadRenameTarget{}, err
	}
	view, found, err := h.resolveCodexSessionByIndex(runtime.bindingKey, index)
	if err != nil {
		return codexThreadRenameTarget{}, err
	}
	if !found {
		return codexThreadRenameTarget{}, fmt.Errorf("会话编号不存在，请先发送 /cx ls 查看当前工作空间会话")
	}
	threadID := strings.TrimSpace(view.ThreadID)
	if threadID == "" || view.PendingNewThread {
		return codexThreadRenameTarget{}, fmt.Errorf("该编号当前没有可重命名的会话")
	}
	return codexThreadRenameTarget{workspaceRoot: workspaceRoot, threadID: threadID}, nil
}

func (h *Handler) validateCodexRenameWorkspace(runtime codexSessionCommandRuntime, workspaceRoot string) error {
	if err := h.hiddenWorkspaceError(runtime.agentName, workspaceRoot, "cx"); err != nil {
		return err
	}
	if runtime.admin || h.isWorkspaceAllowed(workspaceRoot) || h.isConfiguredAgentWorkspace(runtime.agentName, workspaceRoot) {
		return nil
	}
	return fmt.Errorf("该会话工作空间不在允许范围，请发送 /cx ls 重新选择")
}

func (h *Handler) auditSessionRename(platformName string, userID string, agentName string, kind string, sessionID string, status string) {
	h.auditRecord(auditEntry{
		Platform: platformName,
		User:     userID,
		Agent:    agentName,
		Action:   kind + "_session_rename",
		Summary:  "status=" + status + " session=" + redactedSessionIdentifier(sessionID),
	})
}
