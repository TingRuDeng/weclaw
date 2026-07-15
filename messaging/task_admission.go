package messaging

import (
	"context"
	"strings"
	"time"
)

type activeTaskAdmissionStatus uint8

const (
	activeTaskStarted activeTaskAdmissionStatus = iota + 1
	activeTaskQueued
	activeTaskMissing
	activeTaskPendingOccupied
)

type activeTaskAdmission struct {
	status  activeTaskAdmissionStatus
	task    *activeAgentTask
	taskCtx context.Context
}

// beginOrQueueActiveTask 在同一临界区内完成任务启动或后续消息排队。
func (h *Handler) beginOrQueueActiveTask(ctx context.Context, key string, meta activeTaskMeta, pending pendingAgentTask) activeTaskAdmission {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	h.ensureActiveTasksLocked()
	if task := h.activeTasks[key]; task != nil {
		return activeTaskAdmission{status: queuePendingOnTask(task, pending), task: task, taskCtx: ctx}
	}
	task, taskCtx := newActiveAgentTask(ctx, meta)
	h.activeTasks[key] = task
	return activeTaskAdmission{status: activeTaskStarted, task: task, taskCtx: taskCtx}
}

// queuePendingActiveTask 只向仍存在的活动任务排队，不把任务消失误报成队列占用。
func (h *Handler) queuePendingActiveTask(key string, pending pendingAgentTask) activeTaskAdmissionStatus {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	task := h.activeTasks[key]
	if task == nil {
		return activeTaskMissing
	}
	return queuePendingOnTask(task, pending)
}

func queuePendingOnTask(task *activeAgentTask, pending pendingAgentTask) activeTaskAdmissionStatus {
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.pending.message != "" || task.pendingSteering {
		return activeTaskPendingOccupied
	}
	task.pending = pending
	return activeTaskQueued
}

func (h *Handler) ensureActiveTasksLocked() {
	if h.activeTasks == nil {
		h.activeTasks = make(map[string]*activeAgentTask)
	}
}

func newActiveAgentTask(ctx context.Context, meta activeTaskMeta) (*activeAgentTask, context.Context) {
	taskCtx, cancel := context.WithCancel(ctx)
	task := &activeAgentTask{
		cancel: cancel, done: make(chan struct{}), owner: strings.TrimSpace(meta.owner),
		routeUserID: strings.TrimSpace(meta.routeUserID), agentName: strings.TrimSpace(meta.agentName),
		preview: previewPendingCodexMessage(meta.message), messageFingerprint: normalizedTextFingerprint(meta.message),
		startedAt: time.Now(), runtimeOwner: meta.runtimeOwner, ownerRevision: meta.ownerRevision,
		phase: codexTaskRunning, codexThreadID: strings.TrimSpace(meta.codexThreadID),
		codexTurnID: strings.TrimSpace(meta.codexTurnID), inProcessCodexLifecycle: meta.inProcessCodexLifecycle,
	}
	return task, taskCtx
}
