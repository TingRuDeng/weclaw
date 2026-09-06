package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type timeoutApprovalReplier struct {
	*platformtest.Replier
	recorded chan struct{}
}

func (r *timeoutApprovalReplier) RecordApprovalTimeout(context.Context, string, []platform.Choice) error {
	r.recorded <- struct{}{}
	return nil
}

func TestApprovalTimeoutRecordsDenialAndRejectsLateChoice(t *testing.T) {
	h := NewHandler(nil, nil)
	options := []agent.ApprovalOption{{ID: "allow", Kind: "allow_once"}, {ID: "deny", Kind: "reject_once"}}
	pending, err := h.registerPendingApproval("user", "request", options)
	if err != nil {
		t.Fatal(err)
	}
	defer h.clearPendingApproval("user", pending)
	pending.expiresAt = time.Now().Add(-time.Second)
	r := &timeoutApprovalReplier{Replier: platformtest.NewReplier(platform.Capabilities{Text: true}), recorded: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	choice, err := h.waitForPendingApproval(ctx, agentInteractionContextOptions{actorUserID: "user", reply: r}, agent.ApprovalRequest{Options: options}, pending)
	if err != nil || choice != "deny" || deliverPendingApprovalChoice(pending, "allow") {
		t.Fatalf("choice=%q err=%v resolved=%v", choice, err, pending.resolved.Load())
	}
	select {
	case <-r.recorded:
	case <-ctx.Done():
		t.Fatal("timeout denial was not displayed")
	}
}

func TestApprovalChoiceWinsExpiredTimer(t *testing.T) {
	for i := 0; i < 100; i++ {
		h := NewHandler(nil, nil)
		options := []agent.ApprovalOption{{ID: "allow", Kind: "allow_once"}, {ID: "deny", Kind: "reject_once"}}
		pending, err := h.registerPendingApproval("user", "request", options)
		if err != nil {
			t.Fatal(err)
		}
		pending.expiresAt = time.Now().Add(-time.Second)
		if !deliverPendingApprovalChoice(pending, "allow") {
			t.Fatal("choice rejected")
		}
		choice, err := h.waitForPendingApproval(context.Background(), agentInteractionContextOptions{actorUserID: "user"}, agent.ApprovalRequest{Options: options}, pending)
		h.clearPendingApproval("user", pending)
		if err != nil || choice != "allow" {
			t.Fatalf("choice=%q err=%v", choice, err)
		}
	}
}

func TestApprovalTimeoutCompetesWithClickOnce(t *testing.T) {
	for i := 0; i < 100; i++ {
		h := NewHandler(nil, nil)
		options := []agent.ApprovalOption{{ID: "allow", Kind: "allow"}, {ID: "deny", Kind: "deny"}}
		pending, err := h.registerPendingApproval("user", "request", options)
		if err != nil {
			t.Fatal(err)
		}
		pending.expiresAt = time.Now().Add(-time.Second)
		clicked := make(chan bool, 1)
		go func() { clicked <- deliverPendingApprovalChoice(pending, "allow") }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		choice, err := h.waitForPendingApproval(ctx, agentInteractionContextOptions{actorUserID: "user"}, agent.ApprovalRequest{Options: options}, pending)
		cancel()
		won := <-clicked
		h.clearPendingApproval("user", pending)
		if err != nil || (won && choice != "allow") || (!won && choice != "deny") {
			t.Fatalf("click=%v choice=%q err=%v", won, choice, err)
		}
	}
}
