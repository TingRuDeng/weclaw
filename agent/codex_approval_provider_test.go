package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
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

func TestCollectAttachedCodexTurnKeepsWatchingWhenDesktopApprovalRemainsPending(t *testing.T) {
	handled := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
		handled <- struct{}{}
		return "accept", nil
	}))
	a := NewACPAgent(ACPAgentConfig{Command: "mock"})
	event := &codexTurnEvent{TurnID: "turn-1", Approval: &codexApprovalRequest{
		Request: ApprovalRequest{
			RequestID: "request-9",
			Options:   []ApprovalOption{{ID: "accept", Kind: "allow"}, {ID: "decline", Kind: "deny"}},
			StateProbe: func(context.Context) (ApprovalRequestState, error) {
				return ApprovalRequestStatePending, nil
			},
		},
		Respond: func(context.Context, string) error {
			return fmt.Errorf("%w: request 9", ErrCodexDesktopRequestNotFound)
		},
	}}
	type watchResult struct {
		text string
		err  error
	}
	done := make(chan watchResult, 1)
	go func() {
		text, err := a.collectAttachedCodexTurn(ctx, codexThreadWatchOptions{
			threadID: "thread-1", targetTurnID: "turn-1", initialEvents: []*codexTurnEvent{event},
			turnCh: make(chan *codexTurnEvent), reconcile: make(chan time.Time),
		})
		done <- watchResult{text: text, err: err}
	}()

	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("approval handler was not called")
	}
	select {
	case result := <-done:
		t.Fatalf("watcher ended after retryable approval response: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	result := <-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("watcher error = %v, want context cancellation", result.err)
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

func TestHandleCodexApprovalEventRechecksRequestNotFound(t *testing.T) {
	tests := []struct {
		name      string
		state     ApprovalRequestState
		probeErr  error
		wantRetry bool
	}{
		{name: "request remains pending", state: ApprovalRequestStatePending, wantRetry: true},
		{name: "request was handled in app", state: ApprovalRequestStateResolvedExternally},
		{name: "turn already ended", state: ApprovalRequestStateTurnTerminal},
		{name: "state unavailable", state: ApprovalRequestStateUnknown, probeErr: errors.New("refresh unavailable"), wantRetry: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
				return "accept", nil
			})
			probes := 0
			a := NewACPAgent(ACPAgentConfig{Command: "mock"})
			event := &codexTurnEvent{Approval: &codexApprovalRequest{
				Request: ApprovalRequest{
					RequestID: "request-9",
					Options:   []ApprovalOption{{ID: "accept", Kind: "allow"}, {ID: "decline", Kind: "deny"}},
					StateProbe: func(context.Context) (ApprovalRequestState, error) {
						probes++
						return test.state, test.probeErr
					},
				},
				Respond: func(context.Context, string) error {
					return fmt.Errorf("%w: request 9", ErrCodexDesktopRequestNotFound)
				},
			}}

			err := a.handleCodexApprovalEvent(ctx, event)
			if probes != 1 {
				t.Fatalf("StateProbe calls = %d, want 1", probes)
			}
			if test.wantRetry && !errors.Is(err, errCodexApprovalResponsePending) {
				t.Fatalf("handleCodexApprovalEvent() error = %v, want retry", err)
			}
			if !test.wantRetry && err != nil {
				t.Fatalf("handleCodexApprovalEvent() error = %v", err)
			}
		})
	}
}
