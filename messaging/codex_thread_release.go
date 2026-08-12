package messaging

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

func (h *Handler) handleCodexReleaseCommand(runtime codexSessionCommandRuntime) string {
	if len(runtime.fields) != 2 {
		return "用法: /cx release"
	}
	store := h.ensureCodexSessions()
	threadID, pending := store.getThread(runtime.bindingKey, runtime.workspaceRoot)
	threadID = strings.TrimSpace(threadID)
	if threadID == "" && !pending {
		return "当前窗口没有已绑定的 Codex 会话。"
	}
	if threadID != "" {
		unlock, err := h.lockCodexSessionThread(runtime.ctx, threadID, "release")
		if err != nil {
			return "当前 Codex 会话控制繁忙，本次解绑未执行。"
		}
		defer unlock()
	}
	lockedThreadID, lockedPending := store.getThread(runtime.bindingKey, runtime.workspaceRoot)
	lockedThreadID = strings.TrimSpace(lockedThreadID)
	if lockedThreadID != threadID || lockedPending != pending {
		return "Codex 会话绑定状态已变化，本次解绑未执行，请重试。"
	}
	conversationID := buildCodexConversationID(runtime.routeUserID, runtime.agentName, runtime.workspaceRoot)
	if h.hasPendingInteractionForRoute(runtime.actorUserID, runtime.routeUserID) {
		return "当前任务正在等待交互，请先处理当前审批或问答，再解除绑定。"
	}
	if runtimeAgent, ok := runtime.agent.(agent.CodexThreadRuntimeAgent); ok && threadID != "" {
		state, stateErr := runtimeAgent.ReadCodexThreadState(runtime.ctx, conversationID, threadID)
		if stateErr != nil {
			if _, active := h.activeTask(conversationID); active {
				return fmt.Sprintf("暂时无法确认当前任务的交互状态，本次解绑未执行: %v", stateErr)
			}
		} else if state.WaitingOnApproval || state.WaitingOnUserInput {
			return "当前任务正在等待交互，请先处理当前审批或问答，再解除绑定。"
		}
	}
	recoveryReservationID := h.codexFrontendRecoveryReservation(
		conversationID, runtime.routeUserID, lockedThreadID,
	)
	var release codexWorkspaceThreadReleaseResult
	releasePrepared := false
	h.codexFollowerDeliveryMu.Lock()
	detached, detachErr := h.detachCodexFrontendTaskWithPrepare(
		conversationID,
		runtime.routeUserID,
		lockedThreadID,
		func() error {
			releasePrepared = true
			var err error
			release, err = store.releaseWorkspaceThread(
				runtime.bindingKey, runtime.workspaceRoot, recoveryReservationID,
			)
			return err
		},
	)
	if detachErr != nil {
		h.codexFollowerDeliveryMu.Unlock()
		return fmt.Sprintf("解除 Codex 会话绑定失败: %v", detachErr)
	}
	if detached.interaction {
		h.codexFollowerDeliveryMu.Unlock()
		return "当前任务正在处理审批或问答，请先完成交互，再解除绑定。"
	}
	if detached.terminal {
		h.codexFollowerDeliveryMu.Unlock()
		return "当前任务已进入终态，请稍后重试解除绑定。"
	}
	if !releasePrepared {
		var err error
		release, err = store.releaseWorkspaceThread(
			runtime.bindingKey, runtime.workspaceRoot, recoveryReservationID,
		)
		if err != nil {
			h.codexFollowerDeliveryMu.Unlock()
			return fmt.Sprintf("解除 Codex 会话绑定失败: %v", err)
		}
	}
	h.codexFollowerDeliveryMu.Unlock()
	if !release.changed {
		return "当前窗口没有已绑定的 Codex 会话。"
	}
	freezeErr := error(nil)
	if detached.progress != nil {
		freezeErr = detached.progress.detachWithoutTerminal(
			runtime.ctx, "已解除当前窗口的会话绑定；本地 Codex 任务继续运行。",
		)
	}
	if codexAgent, ok := runtime.agent.(agent.CodexThreadAgent); ok {
		codexAgent.ClearCodexThread(conversationID)
	}
	commitErr := store.commitWorkspaceThreadRelease(release)
	if commitErr == nil {
		if outbox := h.currentTerminalOutbox(); outbox != nil {
			h.recoverReleasedCodexFollowerStreams(outbox)
		}
	}
	if commitErr != nil {
		if freezeErr != nil {
			return fmt.Sprintf("已停止当前窗口同步，但解绑提交与进度卡冻结均需重试: %v；%v", commitErr, freezeErr)
		}
		return fmt.Sprintf("已停止当前窗口同步；解绑状态已保存，最终提交将在重启时恢复: %v", commitErr)
	}
	if freezeErr != nil {
		return fmt.Sprintf("已解除当前窗口与 Codex 会话的绑定；本地 Codex 任务继续运行。进度卡冻结失败: %v", freezeErr)
	}
	return "已解除当前窗口与 Codex 会话的绑定；本地 Codex 任务继续运行。"
}
