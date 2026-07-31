package messaging

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexThreadArchiveTarget struct {
	workspaceRoot string
	threadID      string
	displayName   string
}

// handleCodexArchiveCommand 归档 Codex 历史并安全解除当前 frontend 的旧绑定。
func (h *Handler) handleCodexArchiveCommand(runtime codexSessionCommandRuntime) string {
	if len(runtime.fields) != 3 {
		return "用法: /cx archive current|<编号>"
	}
	archiveAgent, ok := runtime.agent.(agent.CodexThreadArchiveAgent)
	if !ok {
		return "当前 Codex Agent 不支持归档，请更新 WeClaw 和 Codex CLI 后重试。"
	}
	target, err := h.resolveCodexArchiveTarget(runtime, runtime.fields[2])
	if err != nil {
		return err.Error()
	}

	unlockThread, err := h.lockCodexSessionThread(runtime.ctx, target.threadID, "archive")
	if err != nil {
		return "当前 Codex 会话控制繁忙，本次归档未执行。"
	}
	defer unlockThread()

	if h.hasNonterminalCodexTaskForThread(target.threadID) {
		return "该 Codex 会话仍有任务运行或状态未确认，请等待任务结束并用 /cx status 核对后再归档。"
	}
	for _, bindingKey := range h.ensureCodexSessions().remoteThreadBindingKeys(target.threadID) {
		if bindingKey != runtime.bindingKey {
			return "该 Codex 会话仍被其他窗口绑定。请先在其他窗口切换或新建会话，再执行归档。"
		}
	}

	release, err := h.ensureCodexSessions().releaseRemoteThread(runtime.bindingKey, target.threadID)
	if err != nil {
		return "无法保存归档前的会话解绑，本次归档未执行：" + sanitizeAgentError(err.Error())
	}
	if err := archiveAgent.ArchiveCodexThread(runtime.ctx, target.threadID); err != nil {
		if errors.Is(err, agent.ErrCodexArchiveOutcomeUnknown) {
			stateErr := h.ensureCodexSessions().markRemoteThreadArchived(target.threadID)
			lines := []string{"Codex 会话归档结果暂时无法确认。"}
			if release.changed {
				lines = append(lines, "为避免继续写入可能已归档的会话，当前窗口已解除该会话绑定。")
			} else {
				lines = append(lines, "当前窗口原有绑定未改变。")
			}
			lines = append(lines, "请发送 /cx ls 重新查看可见会话；若仍可见，可重新选择后再试。")
			if stateErr != nil {
				lines = append(lines,
					"本机归档保护未能持久化；服务重启前请勿重新选择该会话。",
					"错误: "+sanitizeAgentError(stateErr.Error()),
				)
			}
			return wechatCommandText(lines...)
		}
		if rollbackErr := h.ensureCodexSessions().rollbackRemoteThreadRelease(release); rollbackErr != nil {
			return wechatCommandText(
				"Codex 会话归档失败，且原绑定未能安全恢复。",
				"请发送 /cx ls 重新选择会话。",
				"错误: "+sanitizeAgentError(errors.Join(err, rollbackErr).Error()),
			)
		}
		return "Codex 会话归档失败，原绑定已恢复：" + sanitizeAgentError(err.Error())
	}
	stateErr := h.ensureCodexSessions().markRemoteThreadArchived(target.threadID)

	lines := []string{"已归档 Codex 会话。"}
	if target.displayName != "" {
		lines = append(lines, "会话: "+target.displayName)
	}
	lines = append(lines, "历史记录仍保留，可在 Codex App 的归档列表中恢复。")
	if release.changed {
		lines = append(lines, "当前窗口已解除绑定；发送 /cx ls 选择其他会话，或发送 /cx new 新建会话。")
	} else {
		lines = append(lines, "发送 /cx ls 查看剩余会话。")
	}
	if stateErr != nil {
		lines = append(lines,
			"本机归档保护未能持久化；服务重启前请勿重新选择该会话。",
			"错误: "+sanitizeAgentError(stateErr.Error()),
		)
	}
	return wechatCommandText(lines...)
}

func (h *Handler) resolveCodexArchiveTarget(
	runtime codexSessionCommandRuntime,
	value string,
) (codexThreadArchiveTarget, error) {
	value = strings.TrimSpace(value)
	if value == "current" {
		threadID, pending := h.ensureCodexSessions().getThread(runtime.bindingKey, runtime.workspaceRoot)
		threadID = strings.TrimSpace(threadID)
		switch {
		case pending:
			return codexThreadArchiveTarget{}, fmt.Errorf("当前是尚未创建的 Codex 新会话草稿，无需归档")
		case threadID == "":
			return codexThreadArchiveTarget{}, fmt.Errorf("当前窗口尚未绑定 Codex 会话，请先发送 /cx ls 选择")
		default:
			return codexThreadArchiveTarget{
				workspaceRoot: runtime.workspaceRoot,
				threadID:      threadID,
			}, nil
		}
	}

	index, ok := parseCodexListIndex(value)
	if !ok {
		return codexThreadArchiveTarget{}, fmt.Errorf("用法: /cx archive current|<编号>")
	}
	if _, browsing := h.codexBrowseWorkspace(runtime.bindingKey); !browsing {
		return codexThreadArchiveTarget{}, fmt.Errorf("请先发送 /cx ls 并进入工作空间，再按会话编号归档")
	}
	view, found, err := h.resolveCodexSessionByIndex(runtime.bindingKey, index)
	if err != nil {
		return codexThreadArchiveTarget{}, err
	}
	if !found {
		return codexThreadArchiveTarget{}, fmt.Errorf("会话编号不存在，请先发送 /cx ls 查看当前工作空间会话")
	}
	workspaceRoot := normalizeCodexWorkspaceRoot(view.WorkspaceRoot)
	if !runtime.admin &&
		!h.isWorkspaceAllowed(workspaceRoot) &&
		!h.isConfiguredAgentWorkspace(runtime.agentName, workspaceRoot) {
		return codexThreadArchiveTarget{}, fmt.Errorf("该会话工作空间不在允许范围，请发送 /cx ls 重新选择")
	}
	threadID := strings.TrimSpace(view.ThreadID)
	if threadID == "" || view.PendingNewThread {
		return codexThreadArchiveTarget{}, fmt.Errorf("该编号当前没有可归档的会话")
	}
	return codexThreadArchiveTarget{
		workspaceRoot: workspaceRoot,
		threadID:      threadID,
		displayName:   codexSessionDisplayName(view),
	}, nil
}

// hasNonterminalCodexTaskForThread 包含 disconnected/uncertain 状态，归档必须失败关闭。
func (h *Handler) hasNonterminalCodexTaskForThread(threadID string) bool {
	threadID = strings.TrimSpace(threadID)
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	for _, task := range h.tasks.active {
		if task == nil {
			continue
		}
		task.mu.Lock()
		matches := strings.TrimSpace(task.codexThreadID) == threadID &&
			task.phase != codexTaskTerminal
		task.mu.Unlock()
		if matches {
			return true
		}
	}
	return false
}
