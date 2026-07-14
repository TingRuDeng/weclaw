package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDispatchToTurnChReservesCapacityForInterruptedEvent 验证中断终态不会被普通进度占满的水位丢弃。
func TestDispatchToTurnChReservesCapacityForInterruptedEvent(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 16)
	a.turnCh["thread-1"] = turnCh
	for i := 0; i < cap(turnCh)*2; i++ {
		a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "progress", Text: "running"})
	}
	event := &codexTurnEvent{Kind: "interrupted", TurnID: "turn-1"}
	if !a.dispatchToTurnCh("thread-1", event) {
		t.Fatal("interrupted event was dropped")
	}
	assertTurnEventKindPresent(t, turnCh, "interrupted")
}

// TestInterruptedEventEvictsQueuedStartedEvent 验证控制队列满时中断终态可淘汰旧启动通知。
func TestInterruptedEventEvictsQueuedStartedEvent(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 4)
	a.turnCh["thread-1"] = turnCh
	for index := 0; index < cap(turnCh); index++ {
		if !a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "started", TurnID: "turn-1"}) {
			t.Fatalf("started event %d was dropped before channel became full", index)
		}
	}
	if !a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "interrupted", TurnID: "turn-1"}) {
		t.Fatal("interrupted event was dropped behind queued started events")
	}
	assertTurnEventKindPresent(t, turnCh, "interrupted")
}

// TestExplicitUnknownThreadDoesNotFallbackToSoleChannel 验证明示子线程终态不会污染唯一的父线程任务。
func TestExplicitUnknownThreadDoesNotFallbackToSoleChannel(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	parentCh := make(chan *codexTurnEvent, 1)
	a.turnCh["parent-thread"] = parentCh

	dispatched := a.dispatchToTurnCh("child-thread", &codexTurnEvent{
		Kind: "interrupted", TurnID: "child-turn",
	})

	if dispatched {
		t.Fatal("明确属于子线程的终态不应回退到父线程通道")
	}
	if len(parentCh) != 0 {
		t.Fatal("父线程通道收到子线程中断事件")
	}
}

// TestCollectAttachedCodexTurnReturnsStructuredInterruptedError 验证接管 watcher 把中断交给上层核对而不是误报成功。
func TestCollectAttachedCodexTurnReturnsStructuredInterruptedError(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	turnCh <- &codexTurnEvent{Kind: "interrupted", TurnID: "turn-1"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := a.collectAttachedCodexTurn(ctx, codexThreadWatchOptions{
		threadID: "thread-1", targetTurnID: "turn-1",
		turnCh: turnCh, reconcile: make(chan time.Time),
	})
	var interrupted *CodexTurnInterruptedError
	if !errors.As(err, &interrupted) {
		t.Fatalf("watch error=%v，期望 CodexTurnInterruptedError", err)
	}
	if interrupted.ThreadID != "thread-1" || interrupted.TurnID != "turn-1" {
		t.Fatalf("interrupted=%#v，期望保留 thread-1/turn-1", interrupted)
	}
}

// assertTurnEventKindPresent 验证缓冲通道中存在指定事件，避免测试依赖事件排列顺序。
func assertTurnEventKindPresent(t *testing.T, turnCh chan *codexTurnEvent, kind string) {
	t.Helper()
	for len(turnCh) > 0 {
		if (<-turnCh).Kind == kind {
			return
		}
	}
	t.Fatalf("turn channel missing %s event", kind)
}
