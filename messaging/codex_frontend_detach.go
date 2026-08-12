package messaging

import (
	"strings"
	"time"
)

type codexFrontendTaskDetachResult struct {
	progress    *progressSession
	detached    bool
	terminal    bool
	interaction bool
}

func (h *Handler) codexFrontendRecoveryReservation(key string, routeUserID string, threadID string) string {
	key = strings.TrimSpace(key)
	routeUserID = strings.TrimSpace(routeUserID)
	threadID = strings.TrimSpace(threadID)
	if key == "" || routeUserID == "" || threadID == "" {
		return ""
	}
	h.tasks.mu.Lock()
	task := h.tasks.active[key]
	if task == nil {
		h.tasks.mu.Unlock()
		return ""
	}
	task.mu.Lock()
	if task.routeUserID != routeUserID || task.codexThreadID != threadID ||
		task.detached || task.phase == codexTaskTerminal {
		task.mu.Unlock()
		h.tasks.mu.Unlock()
		return ""
	}
	progress := task.progress
	task.mu.Unlock()
	h.tasks.mu.Unlock()
	if progress == nil {
		return ""
	}
	return progress.activeRecoveryReservation()
}

// detachCodexFrontendTask 原子撤销一个消息窗口的任务投递权。
// 外部 watcher 会被取消以释放唯一观察槽；进程内 turn context 保持运行，
// 两种路径都不会 interrupt 共享 Codex turn。
func (h *Handler) detachCodexFrontendTask(key string, routeUserID string, threadID string) codexFrontendTaskDetachResult {
	result, _ := h.detachCodexFrontendTaskWithPrepare(key, routeUserID, threadID, nil)
	return result
}

// detachCodexFrontendTaskWithPrepare 把 release 的持久化准备纳入任务、交互和终态同一栅栏。
// prepare 成功即表示崩溃后可以继续提交解绑；失败时恢复进度卡终态竞争权。
func (h *Handler) detachCodexFrontendTaskWithPrepare(
	key string,
	routeUserID string,
	threadID string,
	prepare func() error,
) (codexFrontendTaskDetachResult, error) {
	return h.detachCodexFrontendTaskWithOptions(key, routeUserID, threadID, false, prepare)
}

// detachCodexFrontendTaskForAuthorizationRevocation 强制撤销该 route 的交互和投递。
// 它只解除消息端观察，不取消或 interrupt 共享 Codex turn。
func (h *Handler) detachCodexFrontendTaskForAuthorizationRevocation(
	key string,
	routeUserID string,
	threadID string,
) codexFrontendTaskDetachResult {
	result, _ := h.detachCodexFrontendTaskWithOptions(key, routeUserID, threadID, true, nil)
	return result
}

func (h *Handler) detachCodexFrontendTaskWithOptions(
	key string,
	routeUserID string,
	threadID string,
	forceInteractionDetach bool,
	prepare func() error,
) (codexFrontendTaskDetachResult, error) {
	key = strings.TrimSpace(key)
	routeUserID = strings.TrimSpace(routeUserID)
	threadID = strings.TrimSpace(threadID)
	if key == "" || routeUserID == "" || threadID == "" {
		return codexFrontendTaskDetachResult{}, nil
	}

	h.tasks.mu.Lock()
	task := h.tasks.active[key]
	if task == nil {
		h.tasks.mu.Unlock()
		return codexFrontendTaskDetachResult{}, nil
	}
	task.mu.Lock()
	if task.routeUserID != routeUserID || task.codexThreadID != threadID {
		task.mu.Unlock()
		h.tasks.mu.Unlock()
		return codexFrontendTaskDetachResult{}, nil
	}
	if task.phase == codexTaskTerminal {
		task.mu.Unlock()
		h.tasks.mu.Unlock()
		return codexFrontendTaskDetachResult{terminal: true}, nil
	}
	if forceInteractionDetach {
		task.interactionLease.forceDetach()
	}
	interactionClaim, ok := task.interactionLease.claimDetach()
	if !ok {
		task.mu.Unlock()
		h.tasks.mu.Unlock()
		return codexFrontendTaskDetachResult{interaction: true}, nil
	}
	progress := task.progress
	if progress != nil && !progress.claimDetachWithoutTerminal() {
		interactionClaim.finish(false)
		task.mu.Unlock()
		h.tasks.mu.Unlock()
		return codexFrontendTaskDetachResult{terminal: true}, nil
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			progress.rollbackDetachWithoutTerminalClaim()
			interactionClaim.finish(false)
			task.mu.Unlock()
			h.tasks.mu.Unlock()
			return codexFrontendTaskDetachResult{}, err
		}
	}

	task.detached = true
	task.pending = pendingAgentTask{}
	task.pendingSteering = false
	next, _ := reduceTaskView(task.view, taskViewEvent{kind: taskViewClosed, at: time.Now()})
	task.view = next
	task.progress = nil
	var cancelWatcher func()
	var detachObserver func()
	var watcherDone <-chan struct{}
	if control := task.externalReservation; control != nil {
		control.mu.Lock()
		wasReserved := control.status == externalCodexTaskReserved
		control.status = externalCodexTaskCanceled
		watcherDone = control.watcherDone
		control.mu.Unlock()
		if wasReserved {
			control.finishWatcher()
		}
		if !task.inProcessCodexLifecycle {
			cancelWatcher = task.cancel
		}
	}
	if task.inProcessCodexLifecycle {
		detachObserver = task.detachCodexObserver
	}
	delete(h.tasks.active, key)
	interactionClaim.finish(true)
	task.mu.Unlock()
	h.tasks.mu.Unlock()
	close(task.done)
	if cancelWatcher != nil {
		cancelWatcher()
		if watcherDone != nil {
			select {
			case <-watcherDone:
			case <-time.After(defaultCodexSessionLockWaitTimeout):
			}
		}
	}
	if detachObserver != nil {
		detachObserver()
	}
	return codexFrontendTaskDetachResult{progress: progress, detached: true}, nil
}
