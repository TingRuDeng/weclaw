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
	state  externalCodexTaskState
	watch  externalCodexTaskWatch
	active bool
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
	mu     sync.Mutex
	status externalCodexTaskReservationStatus
}

// prepareExternalCodexTask 只解析外部任务，不占用观察槽或启动观察器。
func (h *Handler) prepareExternalCodexTask(opts externalCodexTaskOptions) (preparedExternalCodexTask, error) {
	state, watch, found, err := h.resolveExternalCodexTask(opts)
	if err != nil || !found {
		return preparedExternalCodexTask{state: state}, err
	}
	if state.ActiveTurnID == "" {
		return preparedExternalCodexTask{}, fmt.Errorf("Codex App thread 处于 active 状态，但未找到 active turn")
	}
	if opts.reply == nil {
		return preparedExternalCodexTask{}, fmt.Errorf("Codex App thread 正在运行，但当前入口无法接管回推")
	}
	return preparedExternalCodexTask{state: state, watch: watch, active: true}, nil
}

// reserveExternalCodexTask 原子占用 conversation 的观察槽，但暂不启动 goroutine。
func (h *Handler) reserveExternalCodexTask(opts externalCodexTaskOptions, prepared preparedExternalCodexTask) (externalCodexTaskReservation, error) {
	if !prepared.active {
		return externalCodexTaskReservation{}, nil
	}
	taskCtx := h.withAgentInteractions(context.Background(), agentInteractionContextOptions{
		actorUserID: opts.actorUserID, routeUserID: opts.routeUserID, reply: opts.reply,
	})
	runtimeOwner, ownerRevision := externalCodexTaskOwner(prepared.state)
	task, watchCtx, started := h.beginActiveTask(taskCtx, opts.conversationID, activeTaskMeta{
		owner: opts.actorUserID, routeUserID: opts.routeUserID, agentName: opts.agentName,
		message:      firstNonBlank(prepared.state.Preview, "Codex App 本地任务"),
		runtimeOwner: runtimeOwner, ownerRevision: ownerRevision,
		codexThreadID: opts.threadID, codexTurnID: prepared.state.ActiveTurnID,
	})
	if !started {
		return h.reuseExternalCodexTaskReservation(opts, prepared.state, task)
	}
	if prepared.state.Progress != "" {
		task.recordProgress(time.Now(), prepared.state.Progress)
	}
	return externalCodexTaskReservation{
		key: opts.conversationID, task: task,
		control: &externalCodexTaskReservationControl{status: externalCodexTaskReserved},
		runtime: externalCodexTaskRuntime{
			opts: opts, state: prepared.state, watch: prepared.watch, task: task, ctx: watchCtx,
		},
	}, nil
}

// reuseExternalCodexTaskReservation 只复用同一用户、thread 和 turn 的现存观察任务。
func (h *Handler) reuseExternalCodexTaskReservation(opts externalCodexTaskOptions, state externalCodexTaskState, task *activeAgentTask) (externalCodexTaskReservation, error) {
	if sameExternalCodexTask(task, opts, state) {
		return externalCodexTaskReservation{key: opts.conversationID, task: task, reused: true}, nil
	}
	return externalCodexTaskReservation{}, errExternalCodexTaskReservationConflict
}

// activateExternalCodexTaskReservation 激活一次新预留；复用、取消或重复激活均不启动新观察器。
func (h *Handler) activateExternalCodexTaskReservation(reservation externalCodexTaskReservation) {
	if !reservation.claimActivation() {
		return
	}
	go h.runExternalCodexTaskWatcher(reservation.runtime)
}

// cancelExternalCodexTaskReservation 只撤销由本预留新建且尚未激活的任务。
func (h *Handler) cancelExternalCodexTaskReservation(reservation externalCodexTaskReservation) {
	if !reservation.claimCancellation() {
		return
	}
	reservation.task.cancel()
	h.finishActiveTask(reservation.key, reservation.task)
}

func (r externalCodexTaskReservation) claimActivation() bool {
	if r.task == nil || r.reused || r.control == nil {
		return false
	}
	r.control.mu.Lock()
	defer r.control.mu.Unlock()
	if r.control.status != externalCodexTaskReserved {
		return false
	}
	r.control.status = externalCodexTaskActivated
	return true
}

func (r externalCodexTaskReservation) claimCancellation() bool {
	if r.task == nil || r.reused || r.control == nil {
		return false
	}
	r.control.mu.Lock()
	defer r.control.mu.Unlock()
	if r.control.status != externalCodexTaskReserved {
		return false
	}
	r.control.status = externalCodexTaskCanceled
	return true
}

func sameExternalCodexTask(task *activeAgentTask, opts externalCodexTaskOptions, state externalCodexTaskState) bool {
	if task == nil {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.owner == strings.TrimSpace(opts.actorUserID) &&
		task.codexThreadID == strings.TrimSpace(opts.threadID) &&
		task.codexTurnID == strings.TrimSpace(state.ActiveTurnID) &&
		task.phase != codexTaskTerminal
}
