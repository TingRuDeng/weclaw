package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var errExternalCodexTaskReservationConflict = errors.New("当前窗口已有其他 Codex 活动任务")

type preparedExternalCodexTask struct {
	state             externalCodexTaskState
	watch             externalCodexTaskWatch
	active            bool
	confirmedInactive bool
}

type externalCodexTaskReservation struct {
	runtime externalCodexTaskRuntime
	key     string
	task    *activeAgentTask
	reused  bool
	control *externalCodexTaskReservationControl
}

type externalCodexTaskReservationStatus uint8

const (
	externalCodexTaskReserved externalCodexTaskReservationStatus = iota + 1
	externalCodexTaskActivated
	externalCodexTaskCanceled
)

type externalCodexTaskReservationControl struct {
	mu      sync.Mutex
	status  externalCodexTaskReservationStatus
	runtime externalCodexTaskRuntime
}

// prepareExternalCodexTask 只解析外部任务，不占用观察槽或启动观察器。
func (h *Handler) prepareExternalCodexTask(opts externalCodexTaskOptions) (preparedExternalCodexTask, error) {
	resolved := h.resolveExternalCodexTask(opts)
	if resolved.err != nil || !resolved.active {
		return preparedExternalCodexTask{
			state: resolved.state, confirmedInactive: resolved.confirmedInactive,
		}, resolved.err
	}
	if resolved.state.ActiveTurnID == "" {
		return preparedExternalCodexTask{}, fmt.Errorf("共享 Codex thread 处于 active 状态，但未找到 active turn")
	}
	if opts.reply == nil {
		return preparedExternalCodexTask{}, fmt.Errorf("共享 Codex thread 正在运行，但当前入口无法登记回推")
	}
	return preparedExternalCodexTask{
		state: resolved.state, watch: resolved.watch, active: true,
	}, nil
}

// reserveExternalCodexTask 原子占用 conversation 的观察槽，但暂不启动 goroutine。
func (h *Handler) reserveExternalCodexTask(opts externalCodexTaskOptions, prepared preparedExternalCodexTask) (externalCodexTaskReservation, error) {
	if !prepared.active {
		return externalCodexTaskReservation{}, nil
	}
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	h.ensureActiveTasksLocked()
	if task := h.activeTasks[opts.conversationID]; task != nil {
		return h.reuseExternalCodexTaskReservationLocked(opts, prepared.state, task)
	}
	return h.createExternalCodexTaskReservationLocked(opts, prepared), nil
}

// createExternalCodexTaskReservationLocked 在共享槽位锁内创建 reserved 任务及其唯一控制器。
func (h *Handler) createExternalCodexTaskReservationLocked(opts externalCodexTaskOptions, prepared preparedExternalCodexTask) externalCodexTaskReservation {
	taskCtx := h.withAgentInteractions(context.Background(), agentInteractionContextOptions{
		actorUserID: opts.actorUserID, routeUserID: opts.routeUserID,
		agentName: opts.agentName, reply: opts.reply,
	})
	runtimeOwner, ownerRevision := externalCodexTaskOwner(prepared.state)
	task, watchCtx := newActiveAgentTask(taskCtx, activeTaskMeta{
		owner: opts.actorUserID, routeUserID: opts.routeUserID, agentName: opts.agentName,
		message:      firstNonBlank(prepared.state.Preview, "共享 Codex 任务"),
		runtimeOwner: runtimeOwner, ownerRevision: ownerRevision,
		codexThreadID: opts.threadID, codexTurnID: prepared.state.ActiveTurnID,
	})
	control := &externalCodexTaskReservationControl{status: externalCodexTaskReserved}
	task.phase = codexTaskReserved
	task.externalReservation = control
	control.runtime = externalCodexTaskRuntime{
		opts: opts, state: prepared.state, watch: prepared.watch, task: task, ctx: watchCtx,
	}
	if prepared.state.Progress != "" {
		task.recordProgress(time.Now(), prepared.state.Progress)
	}
	h.activeTasks[opts.conversationID] = task
	return externalCodexTaskReservation{
		key: opts.conversationID, task: task, control: control, runtime: control.runtime,
	}
}

// reuseExternalCodexTaskReservationLocked 只复用身份一致且未取消的共享观察生命周期。
func (h *Handler) reuseExternalCodexTaskReservationLocked(opts externalCodexTaskOptions, state externalCodexTaskState, task *activeAgentTask) (externalCodexTaskReservation, error) {
	task.mu.Lock()
	defer task.mu.Unlock()
	control := task.externalReservation
	if control == nil {
		return reuseInProcessCodexTaskReservationLocked(opts, task)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if !reusableExternalCodexTaskStatus(control.status) || !sameExternalCodexTaskIdentityLocked(task, opts, state) {
		return externalCodexTaskReservation{}, errExternalCodexTaskReservationConflict
	}
	return externalCodexTaskReservation{
		runtime: control.runtime, key: opts.conversationID, task: task, reused: true, control: control,
	}, nil
}

// reuseInProcessCodexTaskReservationLocked 只复用显式由本进程 lifecycle 回推的 running 任务。
func reuseInProcessCodexTaskReservationLocked(opts externalCodexTaskOptions, task *activeAgentTask) (externalCodexTaskReservation, error) {
	if !sameInProcessCodexTaskIdentityLocked(task, opts) {
		return externalCodexTaskReservation{}, errExternalCodexTaskReservationConflict
	}
	return externalCodexTaskReservation{
		key: opts.conversationID, task: task, reused: true,
	}, nil
}

// reusableExternalCodexTaskStatus 仅允许 pending reservation 或已启动 watcher 被幂等复用。
func reusableExternalCodexTaskStatus(status externalCodexTaskReservationStatus) bool {
	return status == externalCodexTaskReserved || status == externalCodexTaskActivated
}

// activateExternalCodexTaskReservation 激活或确认共享观察生命周期仍然有效。
func (h *Handler) activateExternalCodexTaskReservation(reservation externalCodexTaskReservation) bool {
	if reservation.task == nil {
		return true
	}
	runtime, activated := h.claimExternalCodexTaskReservationActivation(reservation)
	if !activated {
		return h.externalCodexTaskReservationActive(reservation)
	}
	go h.runExternalCodexTaskWatcher(runtime)
	return true
}

// externalCodexTaskReservationActive 区分已激活复用与已取消、已终态的失效句柄。
func (h *Handler) externalCodexTaskReservationActive(reservation externalCodexTaskReservation) bool {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	if h.activeTasks[reservation.key] != reservation.task {
		return false
	}
	reservation.task.mu.Lock()
	defer reservation.task.mu.Unlock()
	if reservation.control == nil {
		return reservation.reused && reservation.task.phase == codexTaskRunning
	}
	reservation.control.mu.Lock()
	defer reservation.control.mu.Unlock()
	return reservation.task.externalReservation == reservation.control &&
		reservation.task.phase == codexTaskRunning &&
		reservation.control.status == externalCodexTaskActivated
}

// cancelExternalCodexTaskReservation 只撤销由本预留新建且尚未激活的任务。
func (h *Handler) cancelExternalCodexTaskReservation(reservation externalCodexTaskReservation) {
	if reservation.task == nil || reservation.reused || reservation.control == nil {
		return
	}
	h.cancelReservedExternalCodexTask(reservation)
}

// claimExternalCodexTaskReservationActivation 使所有复用句柄竞争同一个 watcher 启动权。
func (h *Handler) claimExternalCodexTaskReservationActivation(reservation externalCodexTaskReservation) (externalCodexTaskRuntime, bool) {
	if reservation.task == nil || reservation.control == nil {
		return externalCodexTaskRuntime{}, false
	}
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	if h.activeTasks[reservation.key] != reservation.task {
		return externalCodexTaskRuntime{}, false
	}
	reservation.task.mu.Lock()
	defer reservation.task.mu.Unlock()
	reservation.control.mu.Lock()
	defer reservation.control.mu.Unlock()
	if reservation.task.externalReservation != reservation.control ||
		reservation.task.phase != codexTaskReserved || reservation.control.status != externalCodexTaskReserved {
		return externalCodexTaskRuntime{}, false
	}
	reservation.control.status = externalCodexTaskActivated
	reservation.task.phase = codexTaskRunning
	return reservation.control.runtime, true
}

// cancelReservedExternalCodexTask 在线性化临界区内标记终态并移除尚未激活的槽位。
func (h *Handler) cancelReservedExternalCodexTask(reservation externalCodexTaskReservation) {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	if h.activeTasks[reservation.key] != reservation.task {
		return
	}
	reservation.task.mu.Lock()
	defer reservation.task.mu.Unlock()
	reservation.control.mu.Lock()
	defer reservation.control.mu.Unlock()
	if reservation.task.externalReservation != reservation.control ||
		reservation.task.phase != codexTaskReserved || reservation.control.status != externalCodexTaskReserved {
		return
	}
	reservation.control.status = externalCodexTaskCanceled
	reservation.task.claimTerminalLocked()
	reservation.task.cancel()
	delete(h.activeTasks, reservation.key)
	close(reservation.task.done)
}

// sameExternalCodexTaskIdentityLocked 校验规范化身份；调用方必须持有 task.mu。
func sameExternalCodexTaskIdentityLocked(task *activeAgentTask, opts externalCodexTaskOptions, state externalCodexTaskState) bool {
	return task.owner == strings.TrimSpace(opts.actorUserID) &&
		task.routeUserID == strings.TrimSpace(opts.routeUserID) &&
		task.agentName == strings.TrimSpace(opts.agentName) &&
		task.codexThreadID == strings.TrimSpace(opts.threadID) &&
		task.codexTurnID == strings.TrimSpace(state.ActiveTurnID) &&
		task.phase != codexTaskTerminal
}

// sameInProcessCodexTaskIdentityLocked 排除来源不明、非 running 或跨窗口的 control=nil 任务。
func sameInProcessCodexTaskIdentityLocked(task *activeAgentTask, opts externalCodexTaskOptions) bool {
	return task.inProcessCodexLifecycle && task.phase == codexTaskRunning &&
		task.owner == strings.TrimSpace(opts.actorUserID) &&
		task.routeUserID == strings.TrimSpace(opts.routeUserID) &&
		task.agentName == strings.TrimSpace(opts.agentName) &&
		task.codexThreadID == strings.TrimSpace(opts.threadID)
}
