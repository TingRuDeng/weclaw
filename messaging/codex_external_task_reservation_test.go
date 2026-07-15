package messaging

import (
	"context"
	"errors"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestReserveExternalCodexTaskRejectsDifferentTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	existing, _, started := h.beginActiveTask(context.Background(), "conversation-1", activeTaskMeta{
		owner: "user-1", agentName: "codex", codexThreadID: "thread-1", codexTurnID: "turn-old",
	})
	if !started {
		t.Fatal("未能建立旧观察任务")
	}
	defer h.finishActiveTask("conversation-1", existing)
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	_, err := h.reserveExternalCodexTask(opts, prepared)
	if !errors.Is(err, errExternalCodexTaskReservationConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestReserveExternalCodexTaskRejectsUnmarkedInProcessTask(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), opts.conversationID, activeTaskMeta{
		owner: opts.actorUserID, routeUserID: opts.routeUserID, agentName: opts.agentName,
		codexThreadID: opts.threadID,
	})
	if !started {
		t.Fatal("未能建立来源不明的 active task")
	}
	defer h.finishActiveTask(opts.conversationID, task)
	_, err := h.reserveExternalCodexTask(opts, prepared)
	if !errors.Is(err, errExternalCodexTaskReservationConflict) {
		t.Fatalf("来源不明的 control=nil task 不应复用，error=%v", err)
	}
}

func TestReserveExternalCodexTaskRejectsNonRunningInProcessTask(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), opts.conversationID, activeTaskMeta{
		owner: opts.actorUserID, routeUserID: opts.routeUserID, agentName: opts.agentName,
		codexThreadID: opts.threadID, inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("未能建立 in-process active task")
	}
	defer h.finishActiveTask(opts.conversationID, task)
	task.mu.Lock()
	task.phase = codexTaskStopping
	task.mu.Unlock()
	_, err := h.reserveExternalCodexTask(opts, prepared)
	if !errors.Is(err, errExternalCodexTaskReservationConflict) {
		t.Fatalf("非 running 的 in-process task 不应复用，error=%v", err)
	}
}

func TestReserveExternalCodexTaskRequiresSameInProcessIdentity(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), opts.conversationID, activeTaskMeta{
		owner: opts.actorUserID, routeUserID: opts.routeUserID, agentName: opts.agentName,
		codexThreadID: opts.threadID, inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("未能建立 in-process active task")
	}
	defer h.finishActiveTask(opts.conversationID, task)
	for name, mutate := range map[string]func(*externalCodexTaskOptions){
		"actor":  func(changed *externalCodexTaskOptions) { changed.actorUserID = "user-2" },
		"route":  func(changed *externalCodexTaskOptions) { changed.routeUserID = "route-2" },
		"agent":  func(changed *externalCodexTaskOptions) { changed.agentName = "codex-other" },
		"thread": func(changed *externalCodexTaskOptions) { changed.threadID = "thread-2" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := opts
			mutate(&changed)
			_, err := h.reserveExternalCodexTask(changed, prepared)
			if !errors.Is(err, errExternalCodexTaskReservationConflict) {
				t.Fatalf("不同 %s 不应复用 in-process lifecycle，error=%v", name, err)
			}
		})
	}
}

func TestCancelExternalCodexTaskReservationRemovesUnstartedTask(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	h.cancelExternalCodexTaskReservation(reservation)
	h.cancelExternalCodexTaskReservation(reservation)
	if _, active := h.activeTask(opts.conversationID); active {
		t.Fatal("取消预留后不应残留 active task")
	}
}

func TestActivateExternalCodexTaskReservationReportsCanceledHandle(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	h.cancelExternalCodexTaskReservation(reservation)
	if h.activateExternalCodexTaskReservation(reservation) {
		t.Fatal("已取消 reservation 不应报告激活成功")
	}
}

func TestReserveExternalCodexTaskReusedHandleActivatesWatcher(t *testing.T) {
	h := NewHandler(nil, nil)
	watchStarted := make(chan struct{})
	watchDone := make(chan struct{})
	defer closeTestChannel(watchDone)
	prepared, opts := testExternalCodexReservationInput(watchStarted, watchDone)
	first, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	reusedOpts, reusedPrepared := opts, prepared
	reusedOpts.actorUserID, reusedOpts.routeUserID = " user-1 ", " route-1 "
	reusedOpts.agentName, reusedOpts.threadID = " codex ", " thread-1 "
	reusedPrepared.state.ActiveTurnID = " turn-1 "
	second, err := h.reserveExternalCodexTask(reusedOpts, reusedPrepared)
	if err != nil {
		t.Fatal(err)
	}
	if !second.reused || second.task != first.task || second.control != first.control {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	h.activateExternalCodexTaskReservation(second)
	waitUntil(t, func() bool { return channelClosed(watchStarted) })
	if task, active := h.activeTask(opts.conversationID); !active || task != first.task {
		t.Fatal("复用句柄激活后应保留并启动原观察任务")
	}
	close(watchDone)
	waitUntil(t, func() bool { _, active := h.activeTask(opts.conversationID); return !active })
}

func TestCancelExternalCodexTaskReservationKeepsActivatedTask(t *testing.T) {
	h := NewHandler(nil, nil)
	watchStarted := make(chan struct{})
	watchDone := make(chan struct{})
	defer closeTestChannel(watchDone)
	prepared, opts := testExternalCodexReservationInput(watchStarted, watchDone)
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	h.activateExternalCodexTaskReservation(reservation)
	h.activateExternalCodexTaskReservation(reservation)
	waitUntil(t, func() bool { return channelClosed(watchStarted) })
	h.cancelExternalCodexTaskReservation(reservation)
	h.cancelExternalCodexTaskReservation(reservation)
	if task, active := h.activeTask(opts.conversationID); !active || task != reservation.task {
		t.Fatal("已激活的观察任务不应被预留取消逻辑清理")
	}
	close(watchDone)
	waitUntil(t, func() bool { _, active := h.activeTask(opts.conversationID); return !active })
}

func TestCancelExternalCodexTaskReservationLinearizesAnotherReserve(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	first, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	first.control.mu.Lock()
	controlLocked := true
	defer func() {
		if controlLocked {
			first.control.mu.Unlock()
		}
	}()
	cancelDone := make(chan struct{})
	go func() {
		h.cancelExternalCodexTaskReservation(first)
		close(cancelDone)
	}()
	waitUntil(t, func() bool {
		if first.task.mu.TryLock() {
			first.task.mu.Unlock()
			return false
		}
		return true
	})
	reserveDone := make(chan externalCodexReservationResult, 1)
	go func() {
		reservation, reserveErr := h.reserveExternalCodexTask(opts, prepared)
		reserveDone <- externalCodexReservationResult{reservation: reservation, err: reserveErr}
	}()
	if len(reserveDone) != 0 {
		t.Fatal("取消临界区释放前，新 reserve 不应穿透共享槽位锁")
	}
	first.control.mu.Unlock()
	controlLocked = false
	waitUntil(t, func() bool { return channelClosed(cancelDone) && len(reserveDone) == 1 })
	result := <-reserveDone
	if result.err != nil || result.reservation.reused || result.reservation.task == first.task {
		t.Fatalf("取消完成后应建立新槽，result=%#v", result)
	}
	h.cancelExternalCodexTaskReservation(result.reservation)
}

type externalCodexReservationResult struct {
	reservation externalCodexTaskReservation
	err         error
}

func TestReserveExternalCodexTaskRequiresSameIdentity(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	first, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer h.cancelExternalCodexTaskReservation(first)
	for name, mutate := range map[string]func(*externalCodexTaskOptions, *preparedExternalCodexTask){
		"actor": func(changed *externalCodexTaskOptions, _ *preparedExternalCodexTask) { changed.actorUserID = "user-2" },
		"route": func(changed *externalCodexTaskOptions, _ *preparedExternalCodexTask) { changed.routeUserID = "route-2" },
		"agent": func(changed *externalCodexTaskOptions, _ *preparedExternalCodexTask) {
			changed.agentName = "codex-other"
		},
		"thread": func(changed *externalCodexTaskOptions, _ *preparedExternalCodexTask) { changed.threadID = "thread-2" },
		"turn": func(_ *externalCodexTaskOptions, changed *preparedExternalCodexTask) {
			changed.state.ActiveTurnID = "turn-2"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changedOpts, changedPrepared := opts, prepared
			mutate(&changedOpts, &changedPrepared)
			_, err := h.reserveExternalCodexTask(changedOpts, changedPrepared)
			if !errors.Is(err, errExternalCodexTaskReservationConflict) {
				t.Fatalf("不同 %s 不应复用观察槽，error=%v", name, err)
			}
		})
	}
}

func testExternalCodexReservationInput(watchStarted chan struct{}, watchDone <-chan struct{}) (preparedExternalCodexTask, externalCodexTaskOptions) {
	watch := func(context.Context, func(string)) (string, error) { return "完成", nil }
	if watchStarted != nil {
		watch = func(context.Context, func(string)) (string, error) {
			close(watchStarted)
			<-watchDone
			return "完成", nil
		}
	}
	prepared := preparedExternalCodexTask{
		active: true, watch: watch,
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}, Controllable: true},
	}
	opts := externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		agentName: "codex", conversationID: "conversation-1", threadID: "thread-1",
		reply: platformtest.NewReplier(platform.Capabilities{Text: true}),
	}
	return prepared, opts
}

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func closeTestChannel(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}
