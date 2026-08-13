package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestHandleCodexApprovalEventUsesProviderResponder(t *testing.T) {
	want := "accept"
	responded := ""
	ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
		return want, nil
	})
	a := NewACPAgent(ACPAgentConfig{Command: "mock"})
	event := &codexTurnEvent{Approval: &codexApprovalRequest{
		Request: ApprovalRequest{Options: []ApprovalOption{
			{ID: "accept", Kind: "allow"}, {ID: "decline", Kind: "deny"},
		}},
		Respond: func(_ context.Context, decision string) error {
			responded = decision
			return nil
		},
	}}

	if err := a.handleCodexApprovalEvent(ctx, event); err != nil {
		t.Fatalf("handleCodexApprovalEvent() error = %v", err)
	}
	if responded != want {
		t.Fatalf("responded = %q", responded)
	}
}

func TestHandleCodexApprovalEventRejectsMissingResponder(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "mock"})
	event := &codexTurnEvent{Approval: &codexApprovalRequest{
		Request: ApprovalRequest{Options: []ApprovalOption{{ID: "decline", Kind: "deny"}}},
	}}

	err := a.handleCodexApprovalEvent(context.Background(), event)
	if err == nil || !errors.Is(err, errCodexApprovalResponderMissing) {
		t.Fatalf("handleCodexApprovalEvent() error = %v", err)
	}
}

func TestHandleCodexApprovalEventSkipsResponderWhenHandledByAnotherFrontend(t *testing.T) {
	ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
		return "", ErrApprovalResolvedExternally
	})
	responded := false
	a := NewACPAgent(ACPAgentConfig{Command: "mock"})
	event := &codexTurnEvent{Approval: &codexApprovalRequest{
		Request: ApprovalRequest{Options: []ApprovalOption{
			{ID: "accept", Kind: "allow"}, {ID: "decline", Kind: "deny"},
		}},
		Respond: func(context.Context, string) error {
			responded = true
			return nil
		},
	}}
	if err := a.handleCodexApprovalEvent(ctx, event); err != nil {
		t.Fatalf("handleCodexApprovalEvent() error = %v", err)
	}
	if responded {
		t.Fatal("provider responder was called after another frontend handled the request")
	}
}

func TestHandleCodexApprovalEventTreatsRequestNotFoundAsConcurrentResolution(t *testing.T) {
	ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
		return "accept", nil
	})
	a := NewACPAgent(ACPAgentConfig{Command: "mock"})
	event := &codexTurnEvent{Approval: &codexApprovalRequest{
		Request: ApprovalRequest{Options: []ApprovalOption{
			{ID: "accept", Kind: "allow"}, {ID: "decline", Kind: "deny"},
		}},
		Respond: func(context.Context, string) error {
			return fmt.Errorf("%w: request 9", ErrCodexDesktopRequestNotFound)
		},
	}}
	if err := a.handleCodexApprovalEvent(ctx, event); err != nil {
		t.Fatalf("handleCodexApprovalEvent() error = %v", err)
	}
}
