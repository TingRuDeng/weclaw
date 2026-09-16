package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCodexTurnRestoresEventsAfterUnsubscribe(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}, Cwd: t.TempDir()})
	seedCodexAppServerThreadBinding(a, "conversation-1", "thread-1", false)
	a.markCodexThreadSubscribed("thread-1")
	subscribed := true
	resumeCalls, startCalls := 0, 0
	responded := make(chan string, 1)
	a.rpcCall = func(ctx context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/unsubscribe":
			subscribed = false
			return json.RawMessage(`{"status":"unsubscribed"}`), nil
		case "thread/resume":
			resumeCalls++
			subscribed = true
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			startCalls++
			// Codex 仍接受未订阅客户端的输入，但只向已订阅客户端发送事件。
			if subscribed {
				a.dispatchToTurnCh("thread-1", subscriptionRecoveryProgress())
				a.dispatchToTurnCh("thread-1", subscriptionRecoveryApproval(responded))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case decision := <-responded:
					if decision != "decline" {
						return nil, fmt.Errorf("approval decision=%q", decision)
					}
				}
				a.dispatchToTurnCh("thread-1", &codexTurnEvent{Delta: "完成", MessagePhase: "final_answer"})
				a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "completed", TurnID: "turn-1"})
			}
			return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
		case "turn/interrupt":
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	if attempted, err := a.UnsubscribeCodexThread(context.Background(), "thread-1"); !attempted || err != nil {
		t.Fatalf("unsubscribe=(%t,%v)", attempted, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	approvals := 0
	ctx = ContextWithApprovalHandler(ctx, func(_ context.Context, req ApprovalRequest) (string, error) {
		approvals++
		if req.RequestID != "approval-1" {
			return "", fmt.Errorf("unexpected request %q", req.RequestID)
		}
		return "decline", nil
	})
	var progress []string
	reply, err := a.chatCodexAppServerControlledTurn(codexAppServerTurnOptions{
		ctx: ctx, conversationID: "conversation-1", message: "继续",
		onProgress: func(text string) { progress = append(progress, text) },
	})
	if err != nil || reply != "完成" || approvals != 1 || !strings.Contains(strings.Join(progress, "\n"), "正在验证修改") {
		t.Fatalf("reply=%q error=%v approvals=%d progress=%q", reply, err, approvals, progress)
	}
	if resumeCalls != 1 || startCalls != 1 || a.codexThreadSubscriptionPending("conversation-1", "thread-1") {
		t.Fatalf("resume=%d start=%d, want one real subscription and one input", resumeCalls, startCalls)
	}
}

func TestCodexActiveWatcherRestoresSubscriptionBeforeReplay(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}, Cwd: t.TempDir()})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	responded := make(chan string, 1)
	resumeCalls := 0
	snapshotRPC := codexThreadSnapshotRPC(t,
		json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"active"}}}`),
		json.RawMessage(`{"data":[{"id":"turn-1","status":"inProgress"}],"nextCursor":null}`),
		map[string]json.RawMessage{"turn-1": json.RawMessage(`{"data":[],"nextCursor":null}`)},
	)
	a.rpcCall = func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method == "thread/resume" {
			resumeCalls++
			// 订阅恢复期间即可收到事件，observer 必须先就绪。
			a.dispatchToTurnCh("thread-1", subscriptionRecoveryProgress())
			a.dispatchToTurnCh("thread-1", subscriptionRecoveryApproval(responded))
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		}
		return snapshotRPC(ctx, method, params)
	}
	if _, err := a.HandoffCodexRuntime(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	approvals := 0
	ctx = ContextWithApprovalHandler(ctx, func(context.Context, ApprovalRequest) (string, error) {
		approvals++
		return "decline", nil
	})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
		case <-responded:
			a.dispatchToTurnCh("thread-1", &codexTurnEvent{Delta: "完成", MessagePhase: "final_answer"})
			a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "completed", TurnID: "turn-1"})
		}
	}()
	var progress []string
	reply, err := a.watchCodexThreadWithReconcile(ctx, codexThreadWatchOptions{
		conversationID: request.Ref.ConversationID, threadID: "thread-1", targetTurnID: "turn-1",
		onProgress: func(text string) { progress = append(progress, text) },
	})
	cancel()
	<-finished
	if err != nil || reply != "完成" || approvals != 1 || !strings.Contains(strings.Join(progress, "\n"), "正在验证修改") {
		t.Fatalf("reply=%q error=%v approvals=%d progress=%q", reply, err, approvals, progress)
	}
	if resumeCalls != 1 {
		t.Fatalf("resumeCalls=%d, want one active-thread subscription", resumeCalls)
	}
}

func subscriptionRecoveryProgress() *codexTurnEvent {
	return &codexTurnEvent{Kind: "progress", TurnID: "turn-1", Progress: &ProgressEvent{
		ID: "progress-1", Kind: ProgressKindCommentary, State: ProgressStateCompleted, Text: "正在验证修改",
	}}
}

func TestCodexSubscriptionFailureReportsDegradedProgress(t *testing.T) {
	for _, watch := range []bool{false, true} {
		t.Run(fmt.Sprintf("watch=%t", watch), func(t *testing.T) {
			a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}, Cwd: t.TempDir()})
			request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
			resumeCalls, startCalls := 0, 0
			snapshotRPC := codexThreadSnapshotRPC(t,
				json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`),
				json.RawMessage(`{"data":[{"id":"turn-1","status":"completed"}],"nextCursor":null}`),
				map[string]json.RawMessage{"turn-1": json.RawMessage(`{"data":[{"turnId":"turn-1","item":{"type":"agentMessage","id":"final-1","text":"完成","phase":"final_answer"}}],"nextCursor":null}`)},
			)
			a.rpcCall = func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
				switch method {
				case "thread/resume":
					resumeCalls++
					return nil, errors.New("subscription unavailable: private diagnostic")
				case "turn/start":
					startCalls++
					return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
				default:
					return snapshotRPC(ctx, method, params)
				}
			}
			if _, err := a.HandoffCodexRuntime(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var events []ProgressEvent
			onProgress := func(event ProgressEvent) { events = append(events, event) }
			var reply string
			var err error
			if watch {
				reply, err = a.watchCodexThreadWithReconcile(ctx, codexThreadWatchOptions{
					conversationID: request.Ref.ConversationID, threadID: "thread-1", targetTurnID: "turn-1", onProgressEvent: onProgress,
				})
			} else {
				reply, err = a.chatCodexAppServerControlledTurn(codexAppServerTurnOptions{
					ctx: ctx, conversationID: request.Ref.ConversationID, message: "继续", onProgressEvent: onProgress,
				})
			}
			if err != nil || reply != "完成" || resumeCalls != 1 || (!watch && startCalls != 1) {
				t.Fatalf("reply=%q err=%v resume=%d start=%d", reply, err, resumeCalls, startCalls)
			}
			if len(events) != 1 || events[0].Kind != ProgressKindStatus || !strings.Contains(events[0].DisplayText(), "进度同步已降级") || strings.Contains(events[0].DisplayText(), "private diagnostic") {
				t.Fatalf("progress=%#v, want a visible synchronization warning without raw diagnostics", events)
			}
			if !a.codexThreadSubscriptionPending(request.Ref.ConversationID, "thread-1") {
				t.Fatal("failed subscription must remain pending even when turn/start succeeds")
			}
		})
	}
}

func TestCodexAdmittedWatcherSubscribesWhileMaintenanceDrains(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}, Cwd: t.TempDir()})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	seedCodexAppServerThreadBinding(a, request.Ref.ConversationID, request.Ref.ThreadID, false)
	maintenanceDone := make(chan error, 1)
	steered := false
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"active"}}}`), nil
		case "thread/turns/list":
			if steered {
				return json.RawMessage(`{"data":[{"id":"turn-1","status":"completed"}],"nextCursor":null}`), nil
			}
			return json.RawMessage(`{"data":[{"id":"turn-1","status":"inProgress"}],"nextCursor":null}`), nil
		case "thread/items/list":
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		case "turn/steer":
			steered = true
			// 维护线程持 admission 等待已受理 turn 排空；observer 不能反向等待它。
			a.codexAdmissionMu.Lock()
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				err := a.ensureCodexAppServerGate().drain(ctx, func(context.Context) error { return nil })
				a.codexAdmissionMu.Unlock()
				maintenanceDone <- err
			}()
			return json.RawMessage(`{"turnId":"turn-1"}`), nil
		case "thread/resume":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.RunCodexTurn(ctx, CodexTurnRequest{Runtime: request, Message: "继续"})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-maintenanceDone; err != nil {
		t.Fatalf("maintenance could not drain the admitted observer: %v", err)
	}
}

func subscriptionRecoveryApproval(responded chan<- string) *codexTurnEvent {
	return &codexTurnEvent{TurnID: "turn-1", Approval: &codexApprovalRequest{
		Request: ApprovalRequest{
			RequestID: "approval-1",
			Options:   []ApprovalOption{{ID: "accept", Kind: "allow"}, {ID: "decline", Kind: "deny"}},
		},
		Respond: func(_ context.Context, decision string) error {
			responded <- decision
			return nil
		},
	}}
}
