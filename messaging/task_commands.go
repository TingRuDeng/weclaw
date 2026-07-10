package messaging

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func runningCodexGuidePrompt() string {
	return "Codex 正在处理上一条任务。\n\n回复 /guide 将此消息作为引导对话发送给 Codex。\n回复 /cancel 撤回该消息。\n不回复时，上一条任务完成后会转为待执行消息。"
}

func runningCodexGuidePromptForTask(task *activeAgentTask) string {
	if task == nil {
		return runningCodexGuidePrompt()
	}
	task.mu.Lock()
	external := task.externalCodex
	task.mu.Unlock()
	if !external {
		return runningCodexGuidePrompt()
	}
	return "Codex App 任务正在进行。\n\n回复 /guide 将此消息发送到当前 Codex App 任务。\n回复 /cancel 撤回该消息。\n不回复时，当前任务完成后会转为待执行消息。"
}

// runnablePendingCodexPrompt 提醒用户确认执行已从引导态转出的暂存消息。
func runnablePendingCodexPrompt(message string) string {
	return "上一条 Codex 任务已完成。\n\n暂存消息：\n" + previewPendingCodexMessage(message) + "\n\n回复“确认”执行该消息。\n回复 /cancel 撤回该消息。"
}

// previewPendingCodexMessage 限制微信提示里的消息预览长度，避免长输入刷屏。
func previewPendingCodexMessage(message string) string {
	runes := []rune(strings.TrimSpace(message))
	if len(runes) <= pendingCodexPreviewRunes {
		return string(runes)
	}
	return string(runes[:pendingCodexPreviewRunes]) + "..."
}

// handlePendingCodexConfirmation 执行用户确认后的待执行 Codex 消息。
func (h *Handler) handlePendingCodexConfirmation(ctx context.Context, platformName platform.PlatformName, accountID string, actorUserID string, routeUserID string, text string, reply platform.Replier, clientID string) bool {
	if !isPendingCodexConfirmText(text) || !h.hasPendingCodexConfirmation() {
		return false
	}
	name, _, key, err := h.codexGuideTargetForRoute(ctx, actorUserID, routeUserID)
	if err != nil {
		sendPlatformText(ctx, reply, actorUserID, err.Error())
		return true
	}
	message, ok, denied := h.takePendingCodexConfirmation(key, actorUserID)
	if denied {
		sendPlatformText(ctx, reply, actorUserID, "只有消息暂存人可以确认执行。")
		return true
	}
	if !ok {
		return false
	}
	h.sendToNamedAgentForAccount(ctx, platformName, accountID, actorUserID, routeUserID, reply, name, message, clientID)
	return true
}

func isPendingCodexConfirmText(text string) bool {
	return strings.TrimSpace(text) == "确认"
}

func (h *Handler) handleGuideCommand(ctx context.Context, platformName platform.PlatformName, accountID string, actorUserID string, routeUserID string, reply platform.Replier, clientID string) {
	name, _, key, err := h.codexGuideTargetForRoute(ctx, actorUserID, routeUserID)
	if err != nil {
		sendPlatformText(ctx, reply, actorUserID, err.Error())
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
	confirmCleared, confirmDenied := h.clearPendingCodexConfirmation(key, actorUserID)
	if confirmDenied {
		return "只有消息暂存人可以撤回该消息。"
	}
	if !cleared && !confirmCleared {
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
	message, threadID, turnID, task, ok, denied := h.takeExternalCodexGuide(key, actor)
	if denied {
		return "只有任务发起人可以发送引导消息。", true
	}
	if !ok {
		return "", false
	}
	if err := runtimeAg.SteerCodexThread(ctx, key, threadID, turnID, message); err != nil {
		h.restorePendingGuide(key, task, message)
		return fmt.Sprintf("发送到当前 Codex App 任务失败: %v", err), true
	}
	task.recordProgress(time.Now(), "已发送引导对话。")
	return "已发送到当前 Codex App 任务。", true
}

func (h *Handler) interruptExternalCodexTask(ctx context.Context, key string, ag agent.Agent, actor string) (string, bool) {
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

// handleListActiveTasks 列出指定用户当前运行中的后台任务，供 /ps 查看。
func (h *Handler) handleListActiveTasks(userID string) string {
	owner := strings.TrimSpace(userID)
	now := time.Now()
	type runningTask struct {
		agentName      string
		preview        string
		elapsed        time.Duration
		lastProgress   string
		lastProgressAt time.Time
	}
	var tasks []runningTask
	h.activeTasksMu.Lock()
	for _, task := range h.activeTasks {
		task.mu.Lock()
		matched := task.owner == owner && !task.detached
		if matched {
			tasks = append(tasks, runningTask{
				agentName:      task.agentName,
				preview:        task.preview,
				elapsed:        now.Sub(task.startedAt),
				lastProgress:   task.lastProgress,
				lastProgressAt: task.lastProgressAt,
			})
		}
		task.mu.Unlock()
	}
	h.activeTasksMu.Unlock()
	if len(tasks) == 0 {
		return "当前没有运行中的任务。"
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].elapsed > tasks[j].elapsed })
	lines := []string{fmt.Sprintf("运行中的任务（%d）：", len(tasks))}
	for i, task := range tasks {
		name := firstNonBlank(task.agentName, "agent")
		line := fmt.Sprintf("%d. %s · 已运行 %s", i+1, name, formatTaskElapsed(task.elapsed))
		if preview := strings.TrimSpace(task.preview); preview != "" {
			line += "\n   " + preview
		}
		if progress := strings.TrimSpace(task.lastProgress); progress != "" {
			line += fmt.Sprintf("\n   最近进展（%s前）：%s", formatTaskElapsed(now.Sub(task.lastProgressAt)), progress)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "\n回复 /stop 停止当前任务。")
	return strings.Join(lines, "\n")
}

// formatTaskElapsed 以分钟/秒粒度展示任务已运行时长。
func formatTaskElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
	return fmt.Sprintf("%d分%d秒", int(d.Minutes()), int(d.Seconds())%60)
}

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
	task.pendingMessage = ""
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
