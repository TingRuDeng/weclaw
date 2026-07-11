package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func runningCodexGuidePrompt() string {
	return "Codex 正在处理上一条任务，此消息已暂存。\n\n回复 /guide 将此消息作为引导对话发送给 Codex。\n回复 /cancel 撤回该消息。\n不操作时，上一条任务结束后会自动执行此消息。"
}

func runningCodexGuidePromptForTask(task *activeAgentTask) string {
	if task == nil {
		return runningCodexGuidePrompt()
	}
	task.mu.Lock()
	external := task.externalCodex
	control := task.externalControl
	task.mu.Unlock()
	if !external {
		return runningCodexGuidePrompt()
	}
	if !control {
		return runningReadOnlyCodexAppPrompt()
	}
	return "Codex App 任务正在进行，此消息已暂存。\n\n回复 /guide 将此消息发送到当前 Codex App 任务。\n回复 /cancel 撤回该消息。\n不操作时，当前任务结束后会自动执行此消息。"
}

func runningReadOnlyCodexAppPrompt() string {
	return "Codex App 本地任务正在进行，此消息已暂存。\n\n当前任务不支持 /guide；回复 /cancel 可撤回此消息。\n不操作时，本地任务结束后会自动执行此消息。"
}

// previewPendingCodexMessage 限制微信提示里的消息预览长度，避免长输入刷屏。
func previewPendingCodexMessage(message string) string {
	runes := []rune(strings.TrimSpace(message))
	if len(runes) <= pendingCodexPreviewRunes {
		return string(runes)
	}
	return string(runes[:pendingCodexPreviewRunes]) + "..."
}

func (h *Handler) handleGuideCommand(ctx context.Context, platformName platform.PlatformName, accountID string, actorUserID string, routeUserID string, reply platform.Replier, clientID string) {
	name, _, key, err := h.codexGuideTargetForRoute(ctx, actorUserID, routeUserID)
	if err != nil {
		sendPlatformText(ctx, reply, actorUserID, err.Error())
		return
	}
	external, control, denied := h.externalCodexControlState(key, actorUserID)
	if denied {
		sendPlatformText(ctx, reply, actorUserID, "只有任务发起人可以发送引导消息。")
		return
	}
	if external && !control {
		sendPlatformText(ctx, reply, actorUserID, "当前 Codex App 本地任务不支持 /guide；暂存消息会在任务结束后自动执行。")
		return
	}
	if text, handled := h.steerPendingGuideToExternalCodex(ctx, key, name, actorUserID); handled {
		sendPlatformText(ctx, reply, actorUserID, text)
		return
	}
	message, task, ok, denied := h.detachPendingGuide(key, actorUserID)
	if denied {
		sendPlatformText(ctx, reply, actorUserID, "只有任务发起人可以发送引导消息。")
		return
	}
	if !ok {
		sendPlatformText(ctx, reply, actorUserID, "当前没有可发送的引导对话。")
		return
	}
	if !waitForActiveTask(ctx, task) {
		return
	}
	h.sendToNamedAgentForAccount(ctx, platformName, accountID, actorUserID, routeUserID, reply, name, message, clientID)
}

func (h *Handler) handleCancelPendingGuide(ctx context.Context, actorUserID string, routeUserID string) string {
	_, _, key, err := h.codexGuideTargetForRoute(ctx, actorUserID, routeUserID)
	if err != nil {
		return err.Error()
	}
	cleared, guideDenied := h.clearPendingGuide(key, actorUserID)
	if guideDenied {
		return "只有任务发起人可以撤回暂存消息。"
	}
	if !cleared {
		return "当前没有可撤回的消息。"
	}
	return "已撤回该消息。"
}

func (h *Handler) handleStopActiveTask(ctx context.Context, actorUserID string, routeUserID string) string {
	_, ag, key, err := h.codexGuideTargetForRoute(ctx, actorUserID, routeUserID)
	if err != nil {
		return err.Error()
	}
	if reply, handled := h.interruptExternalCodexTask(ctx, key, ag, actorUserID); handled {
		return reply
	}
	cancelled, denied := h.cancelActiveTask(key, actorUserID)
	if denied {
		return "只有任务发起人可以停止当前任务。"
	}
	if !cancelled {
		return "当前没有可停止的任务。"
	}
	return "已停止当前任务。"
}

func (h *Handler) steerPendingGuideToExternalCodex(ctx context.Context, key string, agentName string, actor string) (string, bool) {
	ag, err := h.getAgent(ctx, agentName)
	if err != nil {
		return fmt.Sprintf("Codex Agent 不可用: %v", err), true
	}
	runtimeAg, ok := ag.(agent.CodexThreadRuntimeAgent)
	if !ok {
		return "", false
	}
	pending, threadID, turnID, task, ok, denied := h.takeExternalCodexGuide(key, actor)
	if denied {
		return "只有任务发起人可以发送引导消息。", true
	}
	if !ok {
		return "", false
	}
	if err := runtimeAg.SteerCodexThread(ctx, key, threadID, turnID, pending.message); err != nil {
		h.restorePendingGuide(key, task, pending)
		return fmt.Sprintf("发送到当前 Codex App 任务失败: %v", err), true
	}
	task.recordProgress(time.Now(), "已发送引导对话。")
	return "已发送到当前 Codex App 任务。", true
}

func (h *Handler) interruptExternalCodexTask(ctx context.Context, key string, ag agent.Agent, actor string) (string, bool) {
	external, control, denied := h.externalCodexControlState(key, actor)
	if denied {
		return "只有任务发起人可以停止当前任务。", true
	}
	if external && !control {
		return "当前任务由 Codex App 本地进程执行，请在 Codex App 中停止。", true
	}
	runtimeAg, ok := ag.(agent.CodexThreadRuntimeAgent)
	if !ok {
		return "", false
	}
	threadID, turnID, ok, denied := h.externalCodexTurnForTask(key, actor)
	if denied {
		return "只有任务发起人可以停止当前任务。", true
	}
	if !ok {
		return "", false
	}
	if err := runtimeAg.InterruptCodexThread(ctx, key, threadID, turnID); err != nil {
		return fmt.Sprintf("停止当前 Codex App 任务失败: %v", err), true
	}
	h.cancelActiveTask(key, actor)
	return "已停止当前任务。", true
}

// handleCancelCommand 已并入 /cancel(撤回暂存) 与 /stop(停止运行) 两个独立命令，保留占位以便检索历史语义。

func (h *Handler) cancelActiveTask(key string, actor string) (bool, bool) {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return false, false
	}
	task.mu.Lock()
	if task.owner != strings.TrimSpace(actor) {
		task.mu.Unlock()
		h.activeTasksMu.Unlock()
		return false, true
	}
	task.pending = pendingAgentTask{}
	task.detached = true
	cancel := task.cancel
	task.mu.Unlock()
	h.activeTasksMu.Unlock()
	cancel()
	return true, false
}

func (h *Handler) codexGuideTarget(ctx context.Context, userID string) (string, agent.Agent, string, error) {
	return h.codexGuideTargetForRoute(ctx, userID, userID)
}

// codexGuideTargetForRoute 用普通消息相同的 actor/route 规则定位正在运行或暂存的 Codex turn。
func (h *Handler) codexGuideTargetForRoute(ctx context.Context, actorUserID string, routeUserID string) (string, agent.Agent, string, error) {
	name, ok := h.codexAgentName()
	if !ok {
		return "", nil, "", fmt.Errorf("当前没有配置 Codex Agent。")
	}
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		return "", nil, "", fmt.Errorf("Codex Agent 不可用: %v", err)
	}
	return name, ag, h.agentExecutionKeyForRoute(actorUserID, routeUserID, name, ag), nil
}

func waitForActiveTask(ctx context.Context, task *activeAgentTask) bool {
	if task == nil {
		return true
	}
	select {
	case <-task.done:
		return true
	case <-ctx.Done():
		return false
	}
}
