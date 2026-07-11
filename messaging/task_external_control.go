package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type externalCodexControlRequest struct {
	ctx    context.Context
	key    string
	ag     agent.Agent
	actor  string
	action string
}

type externalCodexControlTarget struct {
	task     *activeAgentTask
	threadID string
	turnID   string
}

// externalCodexControlState 返回外部任务是否存在、是否可控制以及当前用户是否无权操作。
func (h *Handler) externalCodexControlState(key string, actor string) (bool, bool, bool) {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return false, false, false
	}
	task.mu.Lock()
	defer h.activeTasksMu.Unlock()
	defer task.mu.Unlock()
	if task.owner != strings.TrimSpace(actor) {
		return task.isExternalCodexLocked(), task.canControlExternalCodexLocked(), true
	}
	return task.isExternalCodexLocked(), task.canControlExternalCodexLocked(), false
}

// resolveExternalCodexControl 每次控制前读取实时 owner 与 active turn。
func (h *Handler) resolveExternalCodexControl(req externalCodexControlRequest) (externalCodexControlTarget, bool, error) {
	target, denied := h.cachedExternalCodexTarget(req.key, req.actor)
	if denied {
		return target, true, fmt.Errorf("只有任务发起人可以控制当前任务")
	}
	if target.task == nil {
		return target, false, nil
	}
	liveAgent, live := req.ag.(agent.CodexLiveRuntimeAgent)
	runtimeAgent, runtime := req.ag.(agent.CodexThreadRuntimeAgent)
	if !live || !runtime {
		if !target.task.canControlExternalCodex() {
			if req.action == "guide" {
				return target, true, fmt.Errorf("当前 Codex App 本地任务不支持 /guide；暂存消息会在任务结束后自动执行")
			}
			return target, true, fmt.Errorf("当前任务由独立 Codex App 进程执行，暂不支持从飞书或微信停止")
		}
		return target, true, nil
	}
	binding, found := liveAgent.CurrentCodexThreadBinding(req.key)
	if !found || binding.Owner != agent.CodexOwnerDesktopLive {
		return target, true, fmt.Errorf("Codex Desktop 实时连接已断开，无法确认%s操作", req.action)
	}
	state, err := runtimeAgent.ReadCodexThreadState(req.ctx, req.key, target.threadID)
	if err != nil {
		return target, true, err
	}
	if !state.Active || state.ActiveTurnID == "" {
		return target, true, fmt.Errorf("Codex Desktop 当前没有可控制的 active turn")
	}
	target.turnID = state.ActiveTurnID
	target.task.refreshExternalCodexTurn(binding, state.ActiveTurnID)
	return target, true, nil
}

func (h *Handler) cachedExternalCodexTarget(key string, actor string) (externalCodexControlTarget, bool) {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	task := h.activeTasks[key]
	if task == nil {
		return externalCodexControlTarget{}, false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.owner != strings.TrimSpace(actor) {
		return externalCodexControlTarget{task: task}, true
	}
	if !task.isExternalCodexLocked() {
		return externalCodexControlTarget{}, false
	}
	return externalCodexControlTarget{task: task, threadID: task.codexThreadID, turnID: task.codexTurnID}, false
}
