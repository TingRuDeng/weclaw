package feishu

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestHandleCardActionEventShowsExpiredWhenApprovalNoLongerPending(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	dispatches := 0

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatches++
		msg.RawCommand.Result <- platform.CardActionResultExpired
	})
	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if dispatches != 1 {
		t.Fatalf("dispatches=%d, want 1", dispatches)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Fatalf("response=%#v, want warning toast", resp)
	}
	assertApprovalCardContent(t, resp, "⚠️ 已过期", "允许本次")
	if cardKit.updateCountFor("card-task-1") != 0 {
		t.Fatalf("expired approval must not update task card, updated=%#v", cardKit.updateCardIDs)
	}

	second, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		t.Fatalf("duplicate expired approval dispatched: %#v", msg)
	})
	if err != nil {
		t.Fatalf("second handleCardActionEvent error: %v", err)
	}
	assertApprovalCardContent(t, second, "⚠️ 已过期", "允许本次")
}
