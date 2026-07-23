package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

const queuedAgentMessage = "已排队，将在当前任务结束后自动执行。"

type taskCommandRequest struct {
	ctx          context.Context
	platformName platform.PlatformName
	accountID    string
	actorUserID  string
	routeUserID  string
	reply        platform.Replier
	clientID     string
}

type taskCommandTarget struct {
	name  string
	agent agent.Agent
	key   string
}

type externalCodexTaskCommand struct {
	ctx       context.Context
	key       string
	agentName string
	agent     agent.Agent
	actor     string
}

// previewPendingCodexMessage 限制微信提示里的消息预览长度，避免长输入刷屏。
func previewPendingCodexMessage(message string) string {
	runes := []rune(strings.TrimSpace(message))
	if len(runes) <= pendingCodexPreviewRunes {
		return string(runes)
	}
	return string(runes[:pendingCodexPreviewRunes]) + "..."
}

func (h *Handler) handleGuideCommand(req taskCommandRequest) {
	target, err := h.resolveTaskCommandTarget(req)
	if err != nil {
		sendPlatformText(req.ctx, req.reply, req.actorUserID, err.Error())
		return
	}
	if isClaudeAgent(target.name, target.agent.Info()) {
		sendPlatformText(req.ctx, req.reply, req.actorUserID, "Claude 当前不支持 /guide；消息可排队并在当前任务结束后自动执行。")
		return
	}
	external, _, denied := h.externalCodexControlState(target.key, req.actorUserID)
	if denied {
		sendPlatformText(req.ctx, req.reply, req.actorUserID, "只有任务发起人可以发送引导消息。")
		return
	}
	if external {
		text, _ := h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{
			ctx: req.ctx, key: target.key, agentName: target.name, actor: req.actorUserID,
		})
		sendPlatformText(req.ctx, req.reply, req.actorUserID, text)
		return
	}
	message, task, ok, denied := h.detachPendingGuide(target.key, req.actorUserID)
	if denied {
		sendPlatformText(req.ctx, req.reply, req.actorUserID, "只有任务发起人可以发送引导消息。")
		return
	}
	if !ok {
		sendPlatformText(req.ctx, req.reply, req.actorUserID, "当前没有可发送的引导对话。")
		return
	}
	if !waitForActiveTask(req.ctx, task) {
		return
	}
	h.sendToNamedAgent(agentMessageRequest{ctx: req.ctx, platformName: req.platformName, accountID: req.accountID, userID: req.actorUserID, routeUserID: req.routeUserID, reply: req.reply, name: target.name, message: message, clientID: req.clientID})

}

func (h *Handler) handleCancelPendingGuide(req taskCommandRequest) string {
	target, err := h.resolveTaskCommandTarget(req)
	if err != nil {
		return err.Error()
	}
	cleared, guideDenied := h.clearPendingGuide(target.key, req.actorUserID)
	if guideDenied {
		return "只有任务发起人可以撤回暂存消息。"
	}
	if !cleared {
		return "当前没有可撤回的消息。"
	}
	return "已撤回该消息。"
}

func (h *Handler) handleStopActiveTask(req taskCommandRequest) string {
	target, err := h.resolveTaskCommandTarget(req)
	if err != nil {
		return err.Error()
	}
	if isCodexAgent(target.name, target.agent.Info()) {
		if reply, handled := h.interruptExternalCodexTask(externalCodexTaskCommand{
			ctx: req.ctx, key: target.key, agent: target.agent, actor: req.actorUserID,
		}); handled {
			return reply
		}
	}
	cancelled, denied := h.cancelActiveTask(target.key, req.actorUserID)
	if denied {
		return "只有任务发起人可以停止当前任务。"
	}
	if !cancelled {
		return "当前没有可停止的任务。"
	}
	return "已停止当前任务。"
}

// steerPendingGuideToExternalCodex 将暂存消息发送到 Codex App 的活动 turn。
func (h *Handler) steerPendingGuideToExternalCodex(req externalCodexTaskCommand) (string, bool) {
	ag, err := h.getAgent(req.ctx, req.agentName)
	if err != nil {
		return fmt.Sprintf("Codex Agent 不可用: %v", err), true
	}
	runtimeAg, ok := ag.(agent.CodexThreadRuntimeAgent)
	if !ok {
		return "", false
	}
	target, handled, resolveErr := h.resolveExternalCodexControl(externalCodexControlRequest{
		ctx: req.ctx, key: req.key, ag: ag, actor: req.actor, action: "guide",
	})
	if handled && resolveErr != nil {
		return resolveErr.Error(), true
	}
	if !handled {
		return "", false
	}
	pending, _, _, task, ok, denied := h.takeExternalCodexGuide(req.key, req.actor)
	if denied {
		return "只有任务发起人可以发送引导消息。", true
	}
	if !ok {
		return "", false
	}
	if err := runtimeAg.SteerCodexThread(req.ctx, req.key, target.threadID, target.turnID, pending.message); err != nil {
		h.finishExternalCodexGuide(req.key, task, false)
		return fmt.Sprintf("发送到当前共享 Codex 任务失败: %v", err), true
	}
	h.finishExternalCodexGuide(req.key, task, true)
	task.recordLocalProgressText(time.Now(), "已发送引导对话。")
	return "已发送到当前共享 Codex 任务。", true
}

// interruptExternalCodexTask 停止共享 host 中由当前任务发起人控制的活动 turn。
func (h *Handler) interruptExternalCodexTask(req externalCodexTaskCommand) (string, bool) {
	runtimeAg, ok := req.agent.(agent.CodexThreadRuntimeAgent)
	if !ok {
		return "", false
	}
	target, handled, err := h.resolveExternalCodexControl(externalCodexControlRequest{
		ctx: req.ctx, key: req.key, ag: req.agent, actor: req.actor, action: "停止",
	})
	if !handled {
		return "", false
	}
	if err != nil {
		return err.Error(), true
	}
	stop := target.task.beginStopRequest(taskStopRequest{actor: req.actor, mode: taskStopRemote})
	switch stop.status {
	case taskStopDenied:
		return "只有任务发起人可以控制当前任务", true
	case taskStopTerminal:
		return "当前任务已经结束，无需停止。", true
	case taskStopAlreadyRequested:
		return "已发送停止请求，等待任务终态。", true
	}
	if err := runtimeAg.InterruptCodexThread(req.ctx, req.key, target.threadID, target.turnID); err != nil {
		target.task.rollbackRemoteStop()
		return fmt.Sprintf("停止当前共享 Codex 任务失败: %v", err), true
	}
	if target.task.commitRemoteStop() == taskStopTerminal {
		return "当前任务已经结束，无需停止。", true
	}
	return "已发送停止请求，等待任务终态。", true
}

// handleCancelCommand 已并入 /cancel(撤回暂存) 与 /stop(停止运行) 两个独立命令，保留占位以便检索历史语义。

func (h *Handler) cancelActiveTask(key string, actor string) (bool, bool) {
	h.tasks.mu.Lock()
	task := h.tasks.active[key]
	if task == nil {
		h.tasks.mu.Unlock()
		return false, false
	}
	stop := task.beginStopRequest(taskStopRequest{
		actor: strings.TrimSpace(actor), detach: true, mode: taskStopLocal,
	})
	h.tasks.mu.Unlock()
	switch stop.status {
	case taskStopDenied:
		return false, true
	case taskStopTerminal:
		return false, false
	case taskStopAlreadyRequested:
		return true, false
	default:
		stop.cancel()
		return true, false
	}
}

// resolveTaskCommandTarget 按当前窗口 Agent 定位任务，避免 Claude 控制命令误发给 Codex。
func (h *Handler) resolveTaskCommandTarget(req taskCommandRequest) (taskCommandTarget, error) {
	name := h.defaultAgentNameForRoute(req.routeUserID, req.platformName, req.accountID)
	if strings.TrimSpace(name) == "" {
		return taskCommandTarget{}, fmt.Errorf("当前窗口没有可控制的 Agent")
	}
	ag, err := h.getAgent(req.ctx, name)
	if err != nil {
		return taskCommandTarget{}, fmt.Errorf("Agent %q 不可用: %v", name, err)
	}
	key := h.agentExecutionKeyForRoute(req.actorUserID, req.routeUserID, name, ag)
	return taskCommandTarget{name: name, agent: ag, key: key}, nil
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
