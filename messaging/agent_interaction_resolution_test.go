package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

type controlledCodexInteractionResolution struct {
	done <-chan struct{}
	err  error
}

func (r controlledCodexInteractionResolution) Done() <-chan struct{} {
	return r.done
}

func (r controlledCodexInteractionResolution) Err() error {
	return r.err
}

func TestApprovalWaitStopsWhenAnotherFrontendResolvesInteraction(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newApprovalKeyCaptureReplier()
	resolved := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := h.approvalHandlerForUser("ou_user", "ou_user", reply)(ctx, agent.ApprovalRequest{
			RequestID: "approval-1",
			Options: []agent.ApprovalOption{
				{ID: "allow", Name: "允许", Kind: "allow"},
				{ID: "deny", Name: "拒绝", Kind: "deny"},
			},
			Resolution: controlledCodexInteractionResolution{
				done: resolved, err: agent.ErrCodexInteractionResolvedExternally,
			},
		})
		result <- err
	}()
	key := reply.waitApprovalKey(t, ctx)
	close(resolved)

	select {
	case err := <-result:
		if !errors.Is(err, agent.ErrCodexInteractionResolvedExternally) {
			t.Fatalf("approval error=%v, want interaction resolved externally", err)
		}
	case <-ctx.Done():
		t.Fatal("approval did not stop after another frontend resolved it")
	}
	if h.consumePendingApprovalForKey("ou_user", key, "allow") {
		t.Fatal("resolved approval remained consumable")
	}
}

func TestUserInputWaitStopsWhenAnotherFrontendResolvesInteraction(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newUserInputCaptureReplier()
	resolved := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := h.userInputHandlerForRoute(interactionTestOptions(reply))(ctx, agent.UserInputRequest{
			RequestID: "input-1",
			Questions: []agent.UserInputQuestion{{
				ID: "question-1", Prompt: "请选择", Options: []agent.UserInputOption{{Label: "继续"}, {Label: "停止"}},
			}},
			Resolution: controlledCodexInteractionResolution{
				done: resolved, err: agent.ErrCodexInteractionResolvedExternally,
			},
		})
		result <- err
	}()
	request := reply.waitRequest(t, ctx)
	close(resolved)

	select {
	case err := <-result:
		if !errors.Is(err, agent.ErrCodexInteractionResolvedExternally) {
			t.Fatalf("user input error=%v, want interaction resolved externally", err)
		}
	case <-ctx.Done():
		t.Fatal("user input did not stop after another frontend resolved it")
	}
	if h.consumePendingApprovalForKey("user-1", request.key, "继续") {
		t.Fatal("resolved user input remained consumable")
	}
}
