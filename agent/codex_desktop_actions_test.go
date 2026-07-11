package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type codexDesktopActionCall struct {
	method string
	params any
}

type codexDesktopActionCaller struct {
	calls  []codexDesktopActionCall
	result json.RawMessage
	err    error
	onCall func(string)
}

func (c *codexDesktopActionCaller) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	c.calls = append(c.calls, codexDesktopActionCall{method: method, params: params})
	if c.onCall != nil {
		c.onCall(method)
	}
	return c.result, c.err
}

func TestCodexDesktopStartTurnMapsFollowerPayload(t *testing.T) {
	caller := &codexDesktopActionCaller{result: json.RawMessage(`{"result":{"turn":{"id":"turn-1"}}}`)}
	actions := newCodexDesktopActions(caller, func() string { return "sender-1" })

	turnID, err := actions.startTurn(context.Background(), codexDesktopStartTurnSpec{
		ConversationID: "thread-1", Input: []codexUserInput{{Type: "text", Text: "继续"}},
		Cwd: "/tmp/project", Model: "gpt-test", Effort: "high",
	})
	if err != nil || turnID != "turn-1" {
		t.Fatalf("startTurn() = %q, %v", turnID, err)
	}
	if len(caller.calls) != 1 || caller.calls[0].method != "thread-follower-start-turn" {
		t.Fatalf("calls = %#v", caller.calls)
	}
	payload := caller.calls[0].params.(codexDesktopStartTurnPayload)
	if payload.ConversationID != "thread-1" || payload.SenderRequestID != "sender-1" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.TurnStartParams.ThreadID != "thread-1" || payload.TurnStartParams.Model != "gpt-test" {
		t.Fatalf("turn params = %#v", payload.TurnStartParams)
	}
}

func TestCodexDesktopStartTurnAcceptsDirectTurnResult(t *testing.T) {
	caller := &codexDesktopActionCaller{result: json.RawMessage(`{"turn":{"id":"turn-2"}}`)}
	actions := newCodexDesktopActions(caller, func() string { return "sender-2" })
	turnID, err := actions.startTurn(context.Background(), codexDesktopStartTurnSpec{ConversationID: "thread-1"})
	if err != nil || turnID != "turn-2" {
		t.Fatalf("startTurn() = %q, %v", turnID, err)
	}
}

func TestCodexDesktopSteerMapsExpectedTurn(t *testing.T) {
	caller := &codexDesktopActionCaller{result: json.RawMessage(`{}`)}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	err := actions.steerTurn(context.Background(), codexDesktopSteerTurnSpec{
		ConversationID: "thread-1", ExpectedTurnID: "turn-1", Message: "补充信息",
	})
	if err != nil {
		t.Fatalf("steerTurn() error = %v", err)
	}
	payload := caller.calls[0].params.(codexDesktopSteerTurnPayload)
	if payload.ConversationID != "thread-1" || payload.ExpectedTurnID != "turn-1" {
		t.Fatalf("payload = %#v", payload)
	}
	if len(payload.Input) != 1 || payload.Input[0].Text != "补充信息" {
		t.Fatalf("input = %#v", payload.Input)
	}
}

func TestCodexDesktopInterruptUsesVersionTwo(t *testing.T) {
	caller := &codexDesktopActionCaller{result: json.RawMessage(`{}`)}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	if err := actions.interruptTurn(context.Background(), "thread-1", "turn-1"); err != nil {
		t.Fatalf("interruptTurn() error = %v", err)
	}
	if caller.calls[0].method != "thread-follower-interrupt-turn" {
		t.Fatalf("method = %s", caller.calls[0].method)
	}
	if codexDesktopMethodVersions[caller.calls[0].method] != 2 {
		t.Fatalf("method version = %d", codexDesktopMethodVersions[caller.calls[0].method])
	}
}

func TestCodexDesktopStartDoesNotRetryDeliveryUnknown(t *testing.T) {
	caller := &codexDesktopActionCaller{err: ErrCodexDesktopDeliveryUnknown}
	actions := newCodexDesktopActions(caller, func() string { return "sender-stable" })
	_, err := actions.startTurn(context.Background(), codexDesktopStartTurnSpec{ConversationID: "thread-1"})
	if !errors.Is(err, ErrCodexDesktopDeliveryUnknown) || len(caller.calls) != 1 {
		t.Fatalf("startTurn() error = %v, calls = %d", err, len(caller.calls))
	}
}

func TestCodexDesktopCommandApprovalUsesCommandDecision(t *testing.T) {
	caller := &codexDesktopActionCaller{result: json.RawMessage(`{}`)}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	event, err := actions.approvalEvent("thread-1", desktopApprovalFixture(
		"request-1", "item/commandExecution/requestApproval",
	))
	if err != nil {
		t.Fatalf("approvalEvent() error = %v", err)
	}
	if err := event.Approval.Respond(context.Background(), "accept"); err != nil {
		t.Fatalf("Respond() error = %v", err)
	}
	if caller.calls[0].method != "thread-follower-command-approval-decision" {
		t.Fatalf("method = %s", caller.calls[0].method)
	}
	assertCodexDesktopApprovalPayload(t, caller.calls[0].params, codexDesktopApprovalPayload{
		ConversationID: "thread-1", RequestID: "request-1", Decision: "accept",
	})
}

func TestCodexDesktopFileAndPermissionApprovalUseFileDecision(t *testing.T) {
	methods := []string{
		"item/fileChange/requestApproval", "item/fileRead/requestApproval",
		"item/permissions/requestApproval",
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			caller := &codexDesktopActionCaller{result: json.RawMessage(`{}`)}
			actions := newCodexDesktopActions(caller, func() string { return "sender" })
			event, err := actions.approvalEvent("thread-1", desktopApprovalFixture("request-1", method))
			if err != nil {
				t.Fatalf("approvalEvent() error = %v", err)
			}
			if err := event.Approval.Respond(context.Background(), "decline"); err != nil {
				t.Fatalf("Respond() error = %v", err)
			}
			if caller.calls[0].method != "thread-follower-file-approval-decision" {
				t.Fatalf("method = %s", caller.calls[0].method)
			}
		})
	}
}

func TestCodexDesktopApprovalRejectsUnknownDecision(t *testing.T) {
	caller := &codexDesktopActionCaller{result: json.RawMessage(`{}`)}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	event, err := actions.approvalEvent("thread-1", desktopApprovalFixture(
		"request-1", "item/fileChange/requestApproval",
	))
	if err != nil {
		t.Fatalf("approvalEvent() error = %v", err)
	}
	if err := event.Approval.Respond(context.Background(), "allow"); err == nil {
		t.Fatal("Respond() error = nil")
	}
	if len(caller.calls) != 0 {
		t.Fatalf("calls = %#v", caller.calls)
	}
}

func TestCodexDesktopPendingApprovalSurvivesDisconnect(t *testing.T) {
	caller := &codexDesktopActionCaller{err: ErrCodexDesktopDisconnected}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	action := desktopApprovalFixture("request-1", "item/commandExecution/requestApproval")
	event, err := actions.approvalEvent("thread-1", action)
	if err != nil {
		t.Fatalf("approvalEvent() error = %v", err)
	}
	if err := event.Approval.Respond(context.Background(), "cancel"); !errors.Is(err, ErrCodexDesktopDisconnected) {
		t.Fatalf("Respond() error = %v", err)
	}
	if action.ID != "request-1" || action.Method == "" {
		t.Fatalf("pending action mutated = %#v", action)
	}
}

func desktopApprovalFixture(requestID string, method string) codexDesktopPendingAction {
	return codexDesktopPendingAction{
		ID: requestID, Method: method,
		Params: map[string]any{
			"threadId": "thread-1", "availableDecisions": []any{"accept", "decline", "cancel"},
			"command": []any{"git", "status"},
		},
	}
}

func assertCodexDesktopApprovalPayload(t *testing.T, value any, expected codexDesktopApprovalPayload) {
	t.Helper()
	payload := value.(codexDesktopApprovalPayload)
	if payload != expected {
		t.Fatalf("payload = %#v", payload)
	}
}
