package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/observability"
)

type activeAgentTask struct {
	mu                      sync.Mutex
	cancel                  context.CancelFunc
	done                    chan struct{}
	detached                bool
	stopRequested           bool
	pending                 pendingAgentTask
	pendingSteering         bool
	owner                   string
	routeUserID             string
	agentName               string
	preview                 string
	messageFingerprint      string
	startedAt               time.Time
	view                    taskViewState
	runtimeOwner            agent.CodexRuntimeHolder
	ownerRevision           uint64
	phase                   codexTaskPhase
	codexThreadID           string
	codexTurnID             string
	externalReservation     *externalCodexTaskReservationControl
	inProcessCodexLifecycle bool
	trace                   observability.TraceContext
	taskID                  string
	conversationID          string
	sessionID               string
	progress                *progressSession
}

type pendingAgentTask struct {
	message    string
	run        func()
	codexRoute codexConversationRoute
}

func (t *activeAgentTask) pendingGuide() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending.message
}

func (h *Handler) beginActiveTask(ctx context.Context, key string, meta activeTaskMeta) (*activeAgentTask, context.Context, bool) {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	h.ensureActiveTasksLocked()
	if h.activeTasks[key] != nil {
		return h.activeTasks[key], ctx, false
	}
	task, taskCtx := newActiveAgentTask(ctx, meta)
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
	owner                   string
	routeUserID             string
	agentName               string
	message                 string
	runtimeOwner            agent.CodexRuntimeHolder
	ownerRevision           uint64
	codexThreadID           string
	codexTurnID             string
	inProcessCodexLifecycle bool
	trace                   observability.TraceContext
	sessionID               string
}

func (h *Handler) finishActiveTask(key string, task *activeAgentTask) {
	h.activeTasksMu.Lock()
	removed := false
	if h.activeTasks[key] == task {
		task.mu.Lock()
		terminal := task.phase == codexTaskTerminal
		task.mu.Unlock()
		if !terminal {
			delete(h.activeTasks, key)
			removed = true
		}
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
	if !task.canControlExternalCodexLocked() || task.pending.message == "" || task.pendingSteering {
		return pendingAgentTask{}, "", "", task, false, false
	}
	pending := task.pending
	task.pendingSteering = true
	return pending, task.codexThreadID, task.codexTurnID, task, true, false
}

// finishExternalCodexGuide 提交或回滚引导发送；发送期间保留槽位，避免第三条消息抢占。
func (h *Handler) finishExternalCodexGuide(key string, task *activeAgentTask, delivered bool) {
	if task == nil {
		return
	}
	h.activeTasksMu.Lock()
	active := h.activeTasks[key] == task
	task.mu.Lock()
	if !task.pendingSteering {
		task.mu.Unlock()
		h.activeTasksMu.Unlock()
		return
	}
	task.pendingSteering = false
	pending := pendingAgentTask{}
	if delivered {
		task.pending = pendingAgentTask{}
	} else if !active {
		pending = task.pending
		task.pending = pendingAgentTask{}
	}
	task.mu.Unlock()
	h.activeTasksMu.Unlock()
	if pending.run != nil {
		pending.run()
	}
}

// completeActiveTask 原子移除运行任务并提升暂存消息，避免收尾时丢失并发输入。
func (h *Handler) completeActiveTask(key string, task *activeAgentTask) (pendingAgentTask, bool) {
	pending, hasPending, claimed := h.claimAndCompleteActiveTask(key, task)
	return pending, claimed && hasPending
}

// claimAndCompleteActiveTask 原子认领终态并移除任务，供多观察源竞争。
func (h *Handler) claimAndCompleteActiveTask(key string, task *activeAgentTask) (pendingAgentTask, bool, bool) {
	if task == nil {
		return pendingAgentTask{}, false, false
	}
	h.activeTasksMu.Lock()
	if h.activeTasks[key] != task {
		h.activeTasksMu.Unlock()
		return pendingAgentTask{}, false, false
	}
	task.mu.Lock()
	if !task.claimTerminalLocked() {
		task.mu.Unlock()
		h.activeTasksMu.Unlock()
		return pendingAgentTask{}, false, false
	}
	pending := pendingAgentTask{}
	if task.phase == codexTaskTerminal && !task.pendingSteering {
		pending = task.pending
		task.pending = pendingAgentTask{}
	}
	delete(h.activeTasks, key)
	task.mu.Unlock()
	h.activeTasksMu.Unlock()
	close(task.done)
	if pending.message == "" || pending.run == nil {
		return pendingAgentTask{}, false, true
	}
	return pending, true, true
}

func (h *Handler) claimActiveTaskTerminal(key string, task *activeAgentTask) bool {
	if task == nil {
		return false
	}
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	if h.activeTasks[key] != task {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.claimTerminalLocked()
}

func (h *Handler) finishClaimedActiveTask(key string, task *activeAgentTask) (pendingAgentTask, bool) {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	if task == nil || h.activeTasks[key] != task {
		return pendingAgentTask{}, false
	}
	task.mu.Lock()
	pending := pendingAgentTask{}
	if task.phase == codexTaskTerminal && !task.pendingSteering {
		pending = task.pending
		task.pending = pendingAgentTask{}
	}
	delete(h.activeTasks, key)
	task.mu.Unlock()
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

func (t *activeAgentTask) attachProgressSession(progress *progressSession) {
	if t == nil || progress == nil {
		return
	}
	t.mu.Lock()
	if t.phase != codexTaskTerminal && !t.view.closed {
		t.progress = progress
	}
	t.mu.Unlock()
}

func (t *activeAgentTask) detachProgressSession(progress *progressSession) {
	if t == nil || progress == nil {
		return
	}
	t.mu.Lock()
	if t.progress == progress {
		t.progress = nil
	}
	t.mu.Unlock()
}

func (t *activeAgentTask) progressReanchorSnapshot() (*progressSession, string, bool) {
	if t == nil {
		return nil, "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.progress == nil || t.detached || t.phase == codexTaskTerminal || t.view.closed {
		return nil, "", false
	}
	return t.progress, t.view.lastProgress, true
}

func (t *activeAgentTask) recordProgress(now time.Time, event agent.ProgressEvent) (string, bool) {
	return t.recordProgressWithPolicy(now, event, false)
}

func (t *activeAgentTask) recordProgressWithPolicy(now time.Time, event agent.ProgressEvent, allowLocalUnsequenced bool) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	next, changed := reduceTaskView(t.view, taskViewEvent{
		kind: taskViewProgress, at: now, progress: event, allowLocalUnsequenced: allowLocalUnsequenced,
	})
	if !changed {
		return "", false
	}
	t.view = next
	return next.lastProgress, true
}

func (t *activeAgentTask) recordProgressText(now time.Time, text string) (string, bool) {
	return t.recordProgress(now, agent.TextProgressEvent(text))
}

// recordLocalProgressText 记录消息层自身产生的状态，不让旧 Agent 字符串回调绕过 source sequence 水位。
func (t *activeAgentTask) recordLocalProgressText(now time.Time, text string) (string, bool) {
	return t.recordProgressWithPolicy(now, agent.TextProgressEvent(text), true)
}

// closeProgress 建立终态水位线，阻止旧 watcher、timer 或回调覆盖最终状态。
func (t *activeAgentTask) closeProgress() {
	if t == nil {
		return
	}
	t.mu.Lock()
	next, _ := reduceTaskView(t.view, taskViewEvent{kind: taskViewClosed, at: time.Now()})
	t.view = next
	t.mu.Unlock()
}

func (t *activeAgentTask) recordTerminalView(now time.Time, state string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	next, _ := reduceTaskView(t.view, taskViewEvent{kind: taskViewTerminal, at: now, terminalState: state})
	t.view = next
	t.mu.Unlock()
}
