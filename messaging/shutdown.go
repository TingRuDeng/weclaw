package messaging

import (
	"context"
	"errors"
)

var (
	ErrActiveTasksRunning = errors.New("active tasks are still running")
	ErrHandlerDraining    = errors.New("handler is draining")
)

// RuntimeDrainResult 描述一次安全排空开始时和有界等待后的任务数量。
type RuntimeDrainResult struct {
	ActiveTasks    int `json:"active_tasks"`
	RemainingTasks int `json:"remaining_tasks"`
}

// Drain 原子关闭任务入口；普通模式遇到活动任务时恢复入口并拒绝，强制模式会取消任务并有界等待。
func (h *Handler) Drain(ctx context.Context, force bool) (RuntimeDrainResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	h.tasks.mu.Lock()
	h.ensureActiveTasksLocked()
	h.tasks.draining = true
	tasks := make([]*activeAgentTask, 0, len(h.tasks.active))
	for _, task := range h.tasks.active {
		task.mu.Lock()
		running := taskIsRunningForStatusLocked(task)
		task.mu.Unlock()
		if running {
			tasks = append(tasks, task)
		}
	}
	result := RuntimeDrainResult{ActiveTasks: len(tasks), RemainingTasks: len(tasks)}
	if len(tasks) > 0 && !force {
		h.tasks.draining = false
		h.tasks.mu.Unlock()
		return result, ErrActiveTasksRunning
	}
	if force {
		for _, task := range tasks {
			task.mu.Lock()
			preserveCodexObserver := task.phase != codexTaskTerminal &&
				((task.externalReservation != nil && !task.inProcessCodexLifecycle) ||
					(task.inProcessCodexLifecycle && task.detachCodexObserver != nil))
			if preserveCodexObserver {
				task.preserveRecoveryOnDrain = true
				task.pending = pendingAgentTask{}
			} else if task.phase != codexTaskTerminal {
				task.phase = codexTaskStopping
				task.stopRequested = true
				task.pending = pendingAgentTask{}
			}
			task.mu.Unlock()
		}
	}
	h.tasks.mu.Unlock()

	if !force || len(tasks) == 0 {
		result.RemainingTasks = 0
		return result, nil
	}
	for _, task := range tasks {
		task.mu.Lock()
		detachObserver := task.preserveRecoveryOnDrain && task.inProcessCodexLifecycle && task.detachCodexObserver != nil
		detachInteractions := task.preserveRecoveryOnDrain
		interactionLease := task.interactionLease
		detach := task.detachCodexObserver
		cancel := task.cancel
		task.mu.Unlock()
		if detachInteractions {
			interactionLease.forceDetach()
		}
		if detachObserver {
			detach()
			continue
		}
		cancel()
	}
	for _, task := range tasks {
		select {
		case <-task.done:
		case <-ctx.Done():
			result.RemainingTasks = h.ActiveTaskCount()
			return result, nil
		}
	}
	result.RemainingTasks = h.ActiveTaskCount()
	return result, nil
}

// CancelDrain 仅供安全重启在外部 supervisor 调用失败时恢复消息入口。
func (h *Handler) CancelDrain() {
	h.tasks.mu.Lock()
	h.tasks.draining = false
	h.tasks.mu.Unlock()
}

func (h *Handler) IsDraining() bool {
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	return h.tasks.draining
}
