package messaging

import (
	"context"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/google/uuid"
)

type activeTaskAdmissionStatus uint8

const (
	activeTaskStarted activeTaskAdmissionStatus = iota + 1
	activeTaskQueued
	activeTaskMissing
	activeTaskPendingOccupied
	activeTaskForeignWriter
)

type activeTaskAdmission struct {
	status  activeTaskAdmissionStatus
	task    *activeAgentTask
	taskCtx context.Context
}

// beginOrQueueClaudeTask turns the active-task slot into the Claude session
// writer lease. The same frontend may queue one continuation; another frontend
// bound to the same session gets an explicit busy result and cannot append work
// to someone else's task lifecycle.
func (h *Handler) beginOrQueueClaudeTask(ctx context.Context, key string, meta activeTaskMeta, pending pendingAgentTask) activeTaskAdmission {
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	h.ensureActiveTasksLocked()
	if task := h.tasks.active[key]; task != nil {
		task.mu.Lock()
		foreign := task.routeUserID != strings.TrimSpace(meta.routeUserID)
		task.mu.Unlock()
		if foreign {
			return activeTaskAdmission{status: activeTaskForeignWriter, task: task, taskCtx: ctx}
		}
		return activeTaskAdmission{status: queuePendingOnTask(task, pending), task: task, taskCtx: ctx}
	}
	task, taskCtx := newActiveAgentTask(ctx, meta)
	h.tasks.active[key] = task
	return activeTaskAdmission{status: activeTaskStarted, task: task, taskCtx: taskCtx}
}

// beginOrQueueActiveTask 在同一临界区内完成任务启动或后续消息排队。
func (h *Handler) beginOrQueueActiveTask(ctx context.Context, key string, meta activeTaskMeta, pending pendingAgentTask) activeTaskAdmission {
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	h.ensureActiveTasksLocked()
	if task := h.tasks.active[key]; task != nil {
		return activeTaskAdmission{status: queuePendingOnTask(task, pending), task: task, taskCtx: ctx}
	}
	task, taskCtx := newActiveAgentTask(ctx, meta)
	h.tasks.active[key] = task
	return activeTaskAdmission{status: activeTaskStarted, task: task, taskCtx: taskCtx}
}

// queuePendingActiveTask 只向仍存在的活动任务排队，不把任务消失误报成队列占用。
func (h *Handler) queuePendingActiveTask(key string, pending pendingAgentTask) activeTaskAdmissionStatus {
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	task := h.tasks.active[key]
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
	h.tasks.ensureLocked()
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
		taskID: uuid.NewString(), conversationID: strings.TrimSpace(meta.trace.ConversationID),
		sessionID: strings.TrimSpace(meta.sessionID),
	}
	task.trace = meta.trace.WithAgent(task.agentName).WithTask(task.taskID).
		WithConversation(task.conversationID).WithSession(task.sessionID).
		WithThreadTurn(task.codexThreadID, task.codexTurnID)
	taskCtx = observability.ContextWithTrace(taskCtx, task.trace)
	return task, taskCtx
}
