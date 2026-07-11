package agent

import (
	"context"
	"errors"
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
