package messaging

import (
	"context"
	"strings"
	"sync"
	"time"
)

type activeAgentTask struct {
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
	detached       bool
	pendingMessage string
	owner          string
	agentName      string
	preview        string
	startedAt      time.Time
	lastProgress   string
	lastProgressAt time.Time
}

func (h *Handler) lockAgentExecution(key string) func() {
	h.taskLocksMu.Lock()
	if h.taskLocks == nil {
		h.taskLocks = make(map[string]*sync.Mutex)
	}
	lock := h.taskLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		h.taskLocks[key] = lock
	}
	h.taskLocksMu.Unlock()

	// 同一执行通道串行进入，避免 Codex 同一 thread 内并发 turn 串结果。
	lock.Lock()
	return lock.Unlock
}

func (h *Handler) beginActiveTask(ctx context.Context, key string, meta activeTaskMeta) (*activeAgentTask, context.Context, bool) {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	if h.activeTasks == nil {
		h.activeTasks = make(map[string]*activeAgentTask)
	}
	if h.activeTasks[key] != nil {
		return h.activeTasks[key], ctx, false
	}
	taskCtx, cancel := context.WithCancel(ctx)
	task := &activeAgentTask{
		cancel:    cancel,
		done:      make(chan struct{}),
		owner:     strings.TrimSpace(meta.owner),
		agentName: strings.TrimSpace(meta.agentName),
		preview:   previewPendingCodexMessage(meta.message),
		startedAt: time.Now(),
	}
	h.activeTasks[key] = task
	return task, taskCtx, true
}

func (h *Handler) activeTask(key string) (*activeAgentTask, bool) {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	task := h.activeTasks[key]
	return task, task != nil
}

// activeTaskMeta 描述一次后台任务的归属信息，供 /ps 和 /cancel 检索。
type activeTaskMeta struct {
	owner     string
	agentName string
	message   string
}

func (h *Handler) finishActiveTask(key string, task *activeAgentTask) {
	h.activeTasksMu.Lock()
	if h.activeTasks[key] == task {
		delete(h.activeTasks, key)
	}
	h.activeTasksMu.Unlock()
	close(task.done)
}

func (h *Handler) storePendingGuide(key string, message string) bool {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	h.activeTasksMu.Unlock()
	if task == nil {
		return false
	}
	task.mu.Lock()
	task.pendingMessage = message
	task.mu.Unlock()
	return true
}

func (h *Handler) detachPendingGuide(key string) (string, *activeAgentTask, bool) {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return "", nil, false
	}

	task.mu.Lock()
	message := task.pendingMessage
	if message == "" {
		task.mu.Unlock()
		h.activeTasksMu.Unlock()
		return "", nil, false
	}
	task.pendingMessage = ""
	task.detached = true
	cancel := task.cancel
	task.mu.Unlock()
	h.activeTasksMu.Unlock()
	cancel()
	return message, task, true
}

func (h *Handler) clearPendingGuide(key string) bool {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	h.activeTasksMu.Unlock()
	if task == nil {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.pendingMessage == "" {
		return false
	}
	task.pendingMessage = ""
	return true
}

// promotePendingGuideToRun 将未处理的引导消息转为待执行消息，避免任务结束后丢失用户输入。
func (h *Handler) promotePendingGuideToRun(key string, task *activeAgentTask) (string, bool) {
	if task == nil {
		return "", false
	}
	task.mu.Lock()
	if task.detached || task.pendingMessage == "" {
		task.mu.Unlock()
		return "", false
	}
	message := task.pendingMessage
	task.pendingMessage = ""
	task.mu.Unlock()
	h.storePendingCodexRun(key, message)
	return message, true
}

// storePendingCodexRun 保存等待用户用 /run 明确确认的 Codex 消息。
func (h *Handler) storePendingCodexRun(key string, message string) {
	h.pendingCodexRunsMu.Lock()
	if h.pendingCodexRuns == nil {
		h.pendingCodexRuns = make(map[string]string)
	}
	h.pendingCodexRuns[key] = message
	h.pendingCodexRunsMu.Unlock()
}

// takePendingCodexRun 取出并删除待执行消息，保证 /run 不会重复执行同一条输入。
func (h *Handler) takePendingCodexRun(key string) (string, bool) {
	h.pendingCodexRunsMu.Lock()
	defer h.pendingCodexRunsMu.Unlock()
	message := h.pendingCodexRuns[key]
	if message == "" {
		return "", false
	}
	delete(h.pendingCodexRuns, key)
	return message, true
}

// clearPendingCodexRun 撤回已经转为待执行状态的 Codex 消息。
func (h *Handler) clearPendingCodexRun(key string) bool {
	h.pendingCodexRunsMu.Lock()
	defer h.pendingCodexRunsMu.Unlock()
	if h.pendingCodexRuns[key] == "" {
		return false
	}
	delete(h.pendingCodexRuns, key)
	return true
}

func (t *activeAgentTask) shouldSendFinal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.detached
}

func (t *activeAgentTask) recordProgress(now time.Time, delta string) {
	progress := previewPendingCodexMessage(delta)
	if progress == "" {
		return
	}
	t.mu.Lock()
	t.lastProgress = progress
	t.lastProgressAt = now
	t.mu.Unlock()
}
