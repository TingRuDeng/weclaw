package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type activeAgentTask struct {
	mu              sync.Mutex
	cancel          context.CancelFunc
	done            chan struct{}
	detached        bool
	pending         pendingAgentTask
	owner           string
	agentName       string
	preview         string
	startedAt       time.Time
	lastProgress    string
	lastProgressAt  time.Time
	externalCodex   bool
	externalControl bool
	codexThreadID   string
	codexTurnID     string
}

type pendingAgentTask struct {
	message string
	run     func()
}

func (t *activeAgentTask) pendingGuide() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending.message
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
		cancel:          cancel,
		done:            make(chan struct{}),
		owner:           strings.TrimSpace(meta.owner),
		agentName:       strings.TrimSpace(meta.agentName),
		preview:         previewPendingCodexMessage(meta.message),
		startedAt:       time.Now(),
		externalCodex:   meta.externalCodex,
		externalControl: meta.externalControl,
		codexThreadID:   strings.TrimSpace(meta.codexThreadID),
		codexTurnID:     strings.TrimSpace(meta.codexTurnID),
	}
	h.activeTasks[key] = task
	return task, taskCtx, true
}

// beginSynchronousActiveTask 登记串行执行的非 Codex 任务，供重启保护和任务状态查询使用。
func (h *Handler) beginSynchronousActiveTask(ctx context.Context, key string, meta activeTaskMeta) (*activeAgentTask, context.Context, error) {
	task, taskCtx, started := h.beginActiveTask(ctx, key, meta)
	if !started {
		return nil, ctx, fmt.Errorf("execution %s already has an active task", key)
	}
	return task, taskCtx, nil
}

func (h *Handler) activeTask(key string) (*activeAgentTask, bool) {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	task := h.activeTasks[key]
	return task, task != nil
}

// activeTaskMeta 描述一次后台任务的归属信息，供 /ps 和 /cancel 检索。
type activeTaskMeta struct {
	owner           string
	agentName       string
	message         string
	externalCodex   bool
	externalControl bool
	codexThreadID   string
	codexTurnID     string
}

func (h *Handler) finishActiveTask(key string, task *activeAgentTask) {
	h.activeTasksMu.Lock()
	removed := false
	if h.activeTasks[key] == task {
		delete(h.activeTasks, key)
		removed = true
	}
	h.activeTasksMu.Unlock()
	if removed {
		close(task.done)
	}
}

func (h *Handler) storePendingGuide(key string, pending pendingAgentTask) bool {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return false
	}
	task.mu.Lock()
	defer h.activeTasksMu.Unlock()
	defer task.mu.Unlock()
	if task.pending.message != "" {
		return false
	}
	task.pending = pending
	return true
}

func (h *Handler) detachPendingGuide(key string, actor string) (string, *activeAgentTask, bool, bool) {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return "", nil, false, false
	}

	task.mu.Lock()
	if task.owner != strings.TrimSpace(actor) {
		task.mu.Unlock()
		h.activeTasksMu.Unlock()
		return "", task, false, true
	}
	message := task.pending.message
	if message == "" {
		task.mu.Unlock()
		h.activeTasksMu.Unlock()
		return "", nil, false, false
	}
	task.pending = pendingAgentTask{}
	task.detached = true
	cancel := task.cancel
	task.mu.Unlock()
	h.activeTasksMu.Unlock()
	cancel()
	return message, task, true, false
}

func (h *Handler) clearPendingGuide(key string, actor string) (bool, bool) {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return false, false
	}
	task.mu.Lock()
	defer h.activeTasksMu.Unlock()
	defer task.mu.Unlock()
	if task.owner != strings.TrimSpace(actor) {
		return false, true
	}
	if task.pending.message == "" {
		return false, false
	}
	task.pending = pendingAgentTask{}
	return true, false
}

func (h *Handler) takeExternalCodexGuide(key string, actor string) (pendingAgentTask, string, string, *activeAgentTask, bool, bool) {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return pendingAgentTask{}, "", "", nil, false, false
	}
	task.mu.Lock()
	defer h.activeTasksMu.Unlock()
	defer task.mu.Unlock()
	if task.owner != strings.TrimSpace(actor) {
		return pendingAgentTask{}, "", "", task, false, true
	}
	if !task.externalCodex || !task.externalControl || task.pending.message == "" || task.codexThreadID == "" || task.codexTurnID == "" {
		return pendingAgentTask{}, "", "", task, false, false
	}
	pending := task.pending
	task.pending = pendingAgentTask{}
	return pending, task.codexThreadID, task.codexTurnID, task, true, false
}

func (h *Handler) restorePendingGuide(key string, task *activeAgentTask, pending pendingAgentTask) {
	if task == nil || strings.TrimSpace(pending.message) == "" {
		return
	}
	h.activeTasksMu.Lock()
	current := h.activeTasks[key]
	h.activeTasksMu.Unlock()
	if current != task {
		return
	}
	task.mu.Lock()
	if task.pending.message == "" {
		task.pending = pending
	}
	task.mu.Unlock()
}

func (h *Handler) externalCodexTurnForTask(key string, actor string) (string, string, bool, bool) {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return "", "", false, false
	}
	task.mu.Lock()
	defer h.activeTasksMu.Unlock()
	defer task.mu.Unlock()
	if task.owner != strings.TrimSpace(actor) {
		return "", "", false, true
	}
	if !task.externalCodex || !task.externalControl || task.codexThreadID == "" || task.codexTurnID == "" {
		return "", "", false, false
	}
	return task.codexThreadID, task.codexTurnID, true, false
}

// completeActiveTask 原子移除运行任务并提升暂存消息，避免收尾时丢失并发输入。
func (h *Handler) completeActiveTask(key string, task *activeAgentTask) (pendingAgentTask, bool) {
	if task == nil {
		return pendingAgentTask{}, false
	}
	h.activeTasksMu.Lock()
	if h.activeTasks[key] != task {
		h.activeTasksMu.Unlock()
		return pendingAgentTask{}, false
	}
	task.mu.Lock()
	pending := pendingAgentTask{}
	if !task.detached {
		pending = task.pending
	}
	task.pending = pendingAgentTask{}
	delete(h.activeTasks, key)
	task.mu.Unlock()
	h.activeTasksMu.Unlock()
	close(task.done)
	if pending.message == "" || pending.run == nil {
		return pendingAgentTask{}, false
	}
	return pending, true
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
