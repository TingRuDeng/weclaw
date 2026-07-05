package feishu

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestRecordApprovalActionPurgesExpired(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	now := time.Unix(0, 0)
	adapter.now = func() time.Time { return now }

	first := parsedCardAction{Action: cardActionChoice, Kind: cardKindApproval, Approval: "appr-1", UserID: "ou_1"}
	if _, ok := adapter.recordApprovalAction(first); !ok {
		t.Fatal("first approval should be recorded as new")
	}
	if len(adapter.approvals) != 1 {
		t.Fatalf("expected 1 record, got %d", len(adapter.approvals))
	}

	// 超过 TTL 后，新审批写入时应清掉过期的旧记录
	now = now.Add(feishuApprovalTTL + time.Minute)
	second := parsedCardAction{Action: cardActionChoice, Kind: cardKindApproval, Approval: "appr-2", UserID: "ou_1"}
	if _, ok := adapter.recordApprovalAction(second); !ok {
		t.Fatal("second approval should be recorded as new")
	}
	if len(adapter.approvals) != 1 {
		t.Fatalf("expired approval not purged: map size=%d", len(adapter.approvals))
	}
	if _, ok := adapter.approvals["approval\x00appr-1"]; ok {
		t.Fatal("expired approval key should have been purged")
	}
}

func TestHandleCardActionEventIsIdempotentForApproval(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := approvalCardActionEvent("allow", "允许本次", "")
	dispatched := make(chan platform.IncomingMessage, 2)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	}

	first, err := adapter.handleCardActionEvent(context.Background(), event, dispatch)
	if err != nil {
		t.Fatalf("first handleCardActionEvent error: %v", err)
	}
	second, err := adapter.handleCardActionEvent(context.Background(), event, dispatch)
	if err != nil {
		t.Fatalf("second handleCardActionEvent error: %v", err)
	}
	if first == nil || first.Card == nil || second == nil || second.Card == nil {
		t.Fatalf("responses first=%#v second=%#v, want compact cards", first, second)
	}
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first dispatch")
	}
	select {
	case msg := <-dispatched:
		t.Fatalf("duplicate approval dispatched: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleCardActionEventConcurrentApprovalDispatchesOnce(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := approvalCardActionEvent("allow", "允许本次", "")
	dispatched := make(chan platform.IncomingMessage, 16)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	}
	var wg sync.WaitGroup

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := adapter.handleCardActionEvent(context.Background(), event, dispatch)
			if err != nil {
				t.Errorf("handleCardActionEvent error: %v", err)
			}
			if resp == nil || resp.Card == nil {
				t.Errorf("response=%#v, want compact card", resp)
			}
		}()
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	if got := len(dispatched); got != 1 {
		t.Fatalf("dispatch count=%d, want 1", got)
	}
}

func TestHandleCardActionEventSecondApprovalDoesNotOverwriteFirstDecision(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	allowEvent := approvalCardActionEvent("allow", "允许本次", "")
	denyEvent := approvalCardActionEvent("deny", "拒绝", "")
	dispatched := make(chan platform.IncomingMessage, 2)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	}

	first, err := adapter.handleCardActionEvent(context.Background(), allowEvent, dispatch)
	if err != nil {
		t.Fatalf("first handleCardActionEvent error: %v", err)
	}
	second, err := adapter.handleCardActionEvent(context.Background(), denyEvent, dispatch)
	if err != nil {
		t.Fatalf("second handleCardActionEvent error: %v", err)
	}

	assertApprovalCardContent(t, first, "✅ 已授权", "允许本次")
	assertApprovalCardContent(t, second, "✅ 已授权", "允许本次")
	select {
	case msg := <-dispatched:
		if msg.RawCommand.Value["choice"] != "allow" {
			t.Fatalf("dispatched choice=%q, want first allow", msg.RawCommand.Value["choice"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first dispatch")
	}
	select {
	case msg := <-dispatched:
		t.Fatalf("duplicate approval dispatched: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleCardActionEventCrossUserSameApprovalKeyDispatchesOnce(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	firstEvent := approvalCardActionEvent("allow", "允许本次", "")
	secondEvent := approvalCardActionEvent("deny", "拒绝", "")
	secondEvent.Event.Operator.OpenID = "ou_other"
	dispatched := make(chan platform.IncomingMessage, 2)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	}

	first, err := adapter.handleCardActionEvent(context.Background(), firstEvent, dispatch)
	if err != nil {
		t.Fatalf("first handleCardActionEvent error: %v", err)
	}
	second, err := adapter.handleCardActionEvent(context.Background(), secondEvent, dispatch)
	if err != nil {
		t.Fatalf("second handleCardActionEvent error: %v", err)
	}

	assertApprovalCardContent(t, first, "✅ 已授权", "允许本次")
	assertApprovalCardContent(t, second, "✅ 已授权", "允许本次")
	select {
	case msg := <-dispatched:
		if msg.UserID != "ou_user" || msg.RawCommand.Value["choice"] != "allow" {
			t.Fatalf("first dispatch msg=%#v, want original allow", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first dispatch")
	}
	select {
	case msg := <-dispatched:
		t.Fatalf("cross-user duplicate approval dispatched: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleCardActionEventNonOwnerDoesNotConsumeApproval(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	ownerEvent := approvalCardActionEvent("allow", "允许本次", "")
	intruderEvent := approvalCardActionEvent("deny", "拒绝", "")
	intruderEvent.Event.Operator.OpenID = "ou_other"
	ownerEvent.Event.Action.Value["approval_owner"] = "ou_user"
	intruderEvent.Event.Action.Value["approval_owner"] = "ou_user"
	dispatched := make(chan platform.IncomingMessage, 2)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	}

	intruder, err := adapter.handleCardActionEvent(context.Background(), intruderEvent, dispatch)
	if err != nil {
		t.Fatalf("intruder handleCardActionEvent error: %v", err)
	}
	select {
	case msg := <-dispatched:
		t.Fatalf("non-owner approval dispatched: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
	owner, err := adapter.handleCardActionEvent(context.Background(), ownerEvent, dispatch)
	if err != nil {
		t.Fatalf("owner handleCardActionEvent error: %v", err)
	}

	if intruder == nil || intruder.Toast == nil || !strings.Contains(intruder.Toast.Content, "任务发起人") {
		t.Fatalf("intruder response=%#v, want owner-only warning toast", intruder)
	}
	assertApprovalCardContent(t, owner, "✅ 已授权", "允许本次")
	select {
	case msg := <-dispatched:
		if msg.UserID != "ou_user" || msg.RawCommand.Value["choice"] != "allow" {
			t.Fatalf("owner dispatch msg=%#v, want owner allow", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for owner dispatch")
	}
}

func TestHandleCardActionEventUsesApprovalKeyWhenMessageIDMissing(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := approvalCardActionEvent("allow", "允许本次", "")
	event.Event.Context.OpenMessageID = ""
	dispatched := make(chan platform.IncomingMessage, 2)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	}

	if _, err := adapter.handleCardActionEvent(context.Background(), event, dispatch); err != nil {
		t.Fatalf("first handleCardActionEvent error: %v", err)
	}
	if _, err := adapter.handleCardActionEvent(context.Background(), event, dispatch); err != nil {
		t.Fatalf("second handleCardActionEvent error: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if got := len(dispatched); got != 1 {
		t.Fatalf("dispatch count=%d, want 1 via approval key fallback", got)
	}
}

func TestApprovalActionKeyFallsBackToMessageIDOnly(t *testing.T) {
	first := parsedCardAction{UserID: "ou_user", MessageID: "om_approval"}
	second := parsedCardAction{UserID: "ou_other", MessageID: "om_approval"}

	if got, want := approvalActionKey(first), approvalActionKey(second); got != want {
		t.Fatalf("approvalActionKey first=%q second=%q, want user-independent message fallback", got, want)
	}
}
