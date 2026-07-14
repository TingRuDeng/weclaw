package messaging

import (
	"context"
	"sync"
	"testing"
)

func TestBeginOrQueueActiveTaskReturnsStructuredStates(t *testing.T) {
	h := NewHandler(nil, nil)
	pending := pendingAgentTask{message: "第二条", run: func() {}}
	first := h.beginOrQueueActiveTask(context.Background(), "task-1", activeTaskMeta{}, pending)
	if first.status != activeTaskStarted || first.task == nil {
		t.Fatalf("first=%#v，期望启动新任务", first)
	}
	second := h.beginOrQueueActiveTask(context.Background(), "task-1", activeTaskMeta{}, pending)
	if second.status != activeTaskQueued || first.task.pendingGuide() != "第二条" {
		t.Fatalf("second=%#v pending=%q，期望排队", second, first.task.pendingGuide())
	}
	third := h.beginOrQueueActiveTask(context.Background(), "task-1", activeTaskMeta{}, pending)
	if third.status != activeTaskPendingOccupied {
		t.Fatalf("third=%#v，期望队列已占用", third)
	}
	if got := h.queuePendingActiveTask("missing", pending); got != activeTaskMissing {
		t.Fatalf("missing=%v，期望任务已消失", got)
	}
}

func TestBeginOrQueueActiveTaskIsAtomic(t *testing.T) {
	h := NewHandler(nil, nil)
	start := make(chan struct{})
	statuses := make(chan activeTaskAdmissionStatus, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result := h.beginOrQueueActiveTask(context.Background(), "shared", activeTaskMeta{}, pendingAgentTask{
				message: "后续消息", run: func() {},
			})
			statuses <- result.status
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	counts := map[activeTaskAdmissionStatus]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[activeTaskStarted] != 1 || counts[activeTaskQueued] != 1 {
		t.Fatalf("statuses=%#v，期望一个启动、一个排队", counts)
	}
}
