package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
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

func TestCodexInteractionBroadcastsToOwnerAndAllFrontendObservers(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	owner := make(chan *codexTurnEvent, 4)
	first := make(chan *codexTurnEvent, 4)
	second := make(chan *codexTurnEvent, 4)
	if !a.registerTurnChannel("thread-1", owner) {
		t.Fatal("failed to register turn owner")
	}
	firstID := a.registerTurnObserver("thread-1", first)
	secondID := a.registerTurnObserver("thread-1", second)
	defer a.unregisterTurnObserver("thread-1", firstID, first)
	defer a.unregisterTurnObserver("thread-1", secondID, second)

	request := &codexApprovalRequest{Request: ApprovalRequest{RequestID: "approval-1"}}
	if !a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "approval_request", Approval: request}) {
		t.Fatal("approval was not delivered to the turn owner")
	}
	if len(owner) != 1 || len(first) != 1 || len(second) != 1 {
		t.Fatalf("owner=%d first=%d second=%d, want 1/1/1", len(owner), len(first), len(second))
	}
	<-owner
	<-first
	<-second
	a.unregisterTurnChannel("thread-1", owner)
	secondRequest := &codexApprovalRequest{Request: ApprovalRequest{RequestID: "approval-2"}}
	if !a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "approval_request", Approval: secondRequest}) {
		t.Fatal("approval was not delivered to frontend observers")
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("first=%d second=%d, want both observer deliveries", len(first), len(second))
	}
}

func TestCodexApprovalBroadcastSubmitsProviderDecisionOnce(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	first := make(chan *codexTurnEvent, 2)
	second := make(chan *codexTurnEvent, 2)
	firstID := a.registerTurnObserver("thread-1", first)
	secondID := a.registerTurnObserver("thread-1", second)
	defer a.unregisterTurnObserver("thread-1", firstID, first)
	defer a.unregisterTurnObserver("thread-1", secondID, second)

	var calls atomic.Int32
	providerEntered := make(chan string, 2)
	releaseProvider := make(chan struct{})
	event := &codexTurnEvent{
		Kind: "approval_request", TurnID: "turn-1",
		Approval: &codexApprovalRequest{
			Request: ApprovalRequest{
				RequestID: "approval-1",
				Options: []ApprovalOption{
					{ID: "allow", Name: "允许"},
					{ID: "deny", Name: "拒绝"},
				},
			},
			Respond: func(_ context.Context, decision string) error {
				calls.Add(1)
				providerEntered <- decision
				<-releaseProvider
				return nil
			},
		},
	}
	if !a.dispatchToTurnCh("thread-1", event) {
		t.Fatal("approval was not broadcast")
	}
	var firstEvent *codexTurnEvent
	select {
	case firstEvent = <-first:
	case <-time.After(time.Second):
		t.Fatal("first observer did not receive approval")
	}
	var secondEvent *codexTurnEvent
	select {
	case secondEvent = <-second:
	case <-time.After(time.Second):
		t.Fatal("second observer did not receive approval")
	}
	results := make(chan error, 2)
	go func() {
		ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
			return "allow", nil
		})
		results <- a.handleCodexApprovalEvent(ctx, firstEvent)
	}()
	go func() {
		ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
			return "deny", nil
		})
		results <- a.handleCodexApprovalEvent(ctx, secondEvent)
	}()

	select {
	case <-providerEntered:
	case <-time.After(time.Second):
		t.Fatal("provider responder was not called")
	}
	select {
	case decision := <-providerEntered:
		close(releaseProvider)
		t.Fatalf("provider responder received a second decision %q", decision)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseProvider)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("approval handler error=%v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls=%d, want 1", got)
	}
}

func TestCodexUserInputBroadcastSubmitsProviderAnswersOnce(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	first := make(chan *codexTurnEvent, 2)
	second := make(chan *codexTurnEvent, 2)
	firstID := a.registerTurnObserver("thread-1", first)
	secondID := a.registerTurnObserver("thread-1", second)
	defer a.unregisterTurnObserver("thread-1", firstID, first)
	defer a.unregisterTurnObserver("thread-1", secondID, second)

	var calls atomic.Int32
	providerEntered := make(chan UserInputAnswers, 2)
	releaseProvider := make(chan struct{})
	event := &codexTurnEvent{
		Kind: "user_input_request", TurnID: "turn-1",
		UserInput: &codexUserInputEvent{
			Request: UserInputRequest{
				RequestID: "input-1",
				Questions: []UserInputQuestion{{
					ID: "question-1", Prompt: "请选择", Options: []UserInputOption{{Label: "继续"}, {Label: "停止"}},
				}},
			},
			Respond: func(_ context.Context, answers UserInputAnswers) error {
				calls.Add(1)
				providerEntered <- answers
				<-releaseProvider
				return nil
			},
		},
	}
	if !a.dispatchToTurnCh("thread-1", event) {
		t.Fatal("user input was not broadcast")
	}
	firstEvent := <-first
	secondEvent := <-second
	results := make(chan error, 2)
	go func() {
		ctx := ContextWithUserInputHandler(context.Background(), func(context.Context, UserInputRequest) (UserInputAnswers, error) {
			return UserInputAnswers{"question-1": {"继续"}}, nil
		})
		results <- a.handleCodexUserInputEvent(ctx, firstEvent)
	}()
	go func() {
		ctx := ContextWithUserInputHandler(context.Background(), func(context.Context, UserInputRequest) (UserInputAnswers, error) {
			return UserInputAnswers{"question-1": {"停止"}}, nil
		})
		results <- a.handleCodexUserInputEvent(ctx, secondEvent)
	}()

	select {
	case <-providerEntered:
	case <-time.After(time.Second):
		t.Fatal("provider responder was not called")
	}
	select {
	case answers := <-providerEntered:
		close(releaseProvider)
		t.Fatalf("provider responder received second answers %#v", answers)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseProvider)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("user input handler error=%v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls=%d, want 1", got)
	}
}

func TestCodexInteractionObserverUnregisterDoesNotDuplicateBroadcast(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	first := make(chan *codexTurnEvent, 2)
	second := make(chan *codexTurnEvent, 2)
	firstID := a.registerTurnObserver("thread-1", first)
	secondID := a.registerTurnObserver("thread-1", second)
	defer a.unregisterTurnObserver("thread-1", secondID, second)
	request := &codexApprovalRequest{Request: ApprovalRequest{RequestID: "approval-1"}}
	event := &codexTurnEvent{Kind: "approval_request", Approval: request}
	if !a.dispatchToTurnCh("thread-1", event) || len(first) != 1 || len(second) != 1 {
		t.Fatalf("initial delivery first=%d second=%d", len(first), len(second))
	}

	a.unregisterTurnObserver("thread-1", firstID, first)
	if len(first) != 0 || len(second) != 1 {
		t.Fatalf("after unregister first=%d second=%d, want 0/1 without duplicate", len(first), len(second))
	}
	if got := <-second; got != event {
		t.Fatalf("broadcast event=%p, want %p", got, event)
	}
}

func TestCodexInteractionRolloverSkipsOldTurnObserversAndRemainsPending(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	type observerResult struct {
		err error
	}
	results := make(chan observerResult, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for range 2 {
		turnCh := make(chan *codexTurnEvent, 1)
		observerID := a.registerTurnObserver("thread-1", turnCh)
		go func() {
			_, err := a.collectAttachedCodexTurn(ctx, codexThreadWatchOptions{
				threadID: "thread-1", targetTurnID: "turn-1",
				turnCh: turnCh, reconcile: make(chan time.Time),
			})
			a.unregisterTurnObserver("thread-1", observerID, turnCh)
			results <- observerResult{err: err}
		}()
	}

	event := &codexTurnEvent{
		Kind: "approval_request", TurnID: "turn-2",
		Approval: &codexApprovalRequest{Request: ApprovalRequest{RequestID: "approval-2"}},
	}
	if !a.dispatchToTurnCh("thread-1", event) {
		t.Fatal("turn-2 approval was not accepted by the observer layer")
	}
	for index := range 2 {
		select {
		case result := <-results:
			if !errors.Is(result.err, ErrCodexControlChanged) {
				t.Fatalf("observer[%d] error=%v, want ErrCodexControlChanged", index, result.err)
			}
		case <-ctx.Done():
			t.Fatalf("observer[%d] did not release the mismatched interaction", index)
		}
	}
	pending := a.claimPendingCodexInteractions("thread-1")
	if len(pending) != 1 || pending[0] != event {
		t.Fatalf("pending interactions=%#v, want the turn-2 approval", pending)
	}
}

func TestCodexObserverMailboxPreservesProgressBurst(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	observer := make(chan *codexTurnEvent, 1)
	id := a.registerTurnObserver("thread-1", observer)
	defer a.unregisterTurnObserver("thread-1", id, observer)
	const total = 300
	for index := 0; index < total; index++ {
		if !a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "progress", ItemID: fmt.Sprintf("item-%03d", index)}) {
			t.Fatalf("progress %d was rejected", index)
		}
	}
	for index := 0; index < total; index++ {
		select {
		case event := <-observer:
			want := fmt.Sprintf("item-%03d", index)
			if event.ItemID != want {
				t.Fatalf("event[%d]=%q, want %q", index, event.ItemID, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("progress %d was lost from the observer mailbox", index)
		}
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
