package messaging

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBeginDrainRefusesActiveTaskAndRestoresAdmission(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{owner: "user-1"})
	if !started {
		t.Fatal("beginActiveTask started=false")
	}

	result, err := h.Drain(context.Background(), false)
	if !errors.Is(err, ErrActiveTasksRunning) || result.ActiveTasks != 1 {
		t.Fatalf("Drain result=%#v err=%v, want active-task refusal", result, err)
	}
	second, _, secondStarted := h.beginActiveTask(context.Background(), "task-2", activeTaskMeta{owner: "user-2"})
	if !secondStarted {
		t.Fatal("failed ordinary drain must restore task admission")
	}
	h.finishActiveTask("task-2", second)
	h.finishActiveTask("task-1", task)
}

func TestForceDrainCancelsTasksWaitsAndRejectsNewAdmission(t *testing.T) {
	h := NewHandler(nil, nil)
	task, taskCtx, started := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{owner: "user-1"})
	if !started {
		t.Fatal("beginActiveTask started=false")
	}
	go func() {
		<-taskCtx.Done()
		h.completeActiveTask("task-1", task)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := h.Drain(ctx, true)
	if err != nil {
		t.Fatalf("Drain force: %v", err)
	}
	if result.ActiveTasks != 1 || result.RemainingTasks != 0 {
		t.Fatalf("Drain result=%#v, want one cancelled and no remaining tasks", result)
	}
	if taskCtx.Err() == nil {
		t.Fatal("force drain did not cancel active task context")
	}
	if next, _, nextStarted := h.beginActiveTask(context.Background(), "task-2", activeTaskMeta{}); nextStarted || next != nil {
		t.Fatal("draining handler accepted a new task")
	}
}
