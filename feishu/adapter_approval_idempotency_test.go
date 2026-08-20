package feishu

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
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

func TestHandleCardActionEventDistinguishesSuccessiveApprovalsOnSameCard(t *testing.T) {
	tests := []struct {
		name         string
		eventIDs     [2]string
		revisions    [2]string
		approvalKeys [2]string
		messageIDs   [2]string
	}{
		{
			name:         "event id",
			eventIDs:     [2]string{"evt_approval_1", "evt_approval_2"},
			revisions:    [2]string{"revision-1", "revision-2"},
			approvalKeys: [2]string{"approval-key-1", "approval-key-2"},
			messageIDs:   [2]string{"om_msg:card-event:evt_approval_1", "om_msg:card-event:evt_approval_2"},
		},
		{
			name:         "card revision fallback",
			revisions:    [2]string{"revision-1", "revision-2"},
			approvalKeys: [2]string{"approval-key-1", "approval-key-2"},
			messageIDs:   [2]string{"om_msg:card-revision:revision-1:choice:allow", "om_msg:card-revision:revision-2:choice:allow"},
		},
		{
			name:         "approval key fallback",
			approvalKeys: [2]string{"approval-key-1", "approval-key-2"},
			messageIDs: [2]string{
				"om_msg:card-approval:fe39ceee9f943b54bd6e72a0e10d91e58de0587f8fecbdd2d0bebb319985fc0c:choice:allow",
				"om_msg:card-approval:92908d40ab32c9d24b2a2372d7bc480ec90a075490bc1268f5cd17ecae3a08db:choice:allow",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
			messageIDs := make([]string, 0, 2)
			for i := range tt.messageIDs {
				event := approvalCardActionEvent("allow", "允许本次", "")
				event.Event.Action.Value["approval_key"] = tt.approvalKeys[i]
				event.Event.Action.Value[cardRevisionValueKey] = tt.revisions[i]
				if tt.eventIDs[i] != "" {
					event.EventV2Base = &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: tt.eventIDs[i]}}
				}
				if _, err := adapter.handleCardActionEvent(context.Background(), event, func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
					messageIDs = append(messageIDs, msg.MessageID)
					consumeApprovalForTest(msg)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if len(messageIDs) != len(tt.messageIDs) {
				t.Fatalf("messageIDs=%#v, want %#v", messageIDs, tt.messageIDs)
			}
			for i, want := range tt.messageIDs {
				if messageIDs[i] != want {
					t.Fatalf("messageIDs[%d]=%q, want %q", i, messageIDs[i], want)
				}
				if strings.Contains(messageIDs[i], tt.approvalKeys[i]) {
					t.Fatalf("messageIDs[%d]=%q, should not contain raw approval key", i, messageIDs[i])
				}
			}
		})
	}
}

func TestHandleCardActionEventDeduplicatesApprovalAcrossNewPlatformEvents(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	first := approvalCardActionEvent("allow", "允许本次", "")
	first.EventV2Base = &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_retry_1"}}
	second := approvalCardActionEvent("allow", "允许本次", "")
	second.EventV2Base = &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "evt_retry_2"}}
	dispatched := make(chan platform.IncomingMessage, 2)
	dispatch := func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	}

	if _, err := adapter.handleCardActionEvent(context.Background(), first, dispatch); err != nil {
		t.Fatalf("first handleCardActionEvent error: %v", err)
	}
	if _, err := adapter.handleCardActionEvent(context.Background(), second, dispatch); err != nil {
		t.Fatalf("second handleCardActionEvent error: %v", err)
	}
	select {
	case msg := <-dispatched:
		if msg.MessageID != "om_msg:card-event:evt_retry_1" {
			t.Fatalf("first MessageID=%q, want event-specific identity", msg.MessageID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first dispatch")
	}
	select {
	case msg := <-dispatched:
		t.Fatalf("retried approval dispatched after new platform event: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleCardActionEventIsIdempotentForApproval(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := approvalCardActionEvent("allow", "允许本次", "")
	dispatched := make(chan platform.IncomingMessage, 2)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
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

func TestHandleCardActionEventDoesNotTreatMissingResultAsSuccess(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := approvalCardActionEvent("allow", "允许本次", "")

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(context.Context, platform.IncomingMessage, platform.Replier) {})
	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Fatalf("response=%#v, want unconfirmed warning", resp)
	}
	assertApprovalCardContent(t, resp, "⚠️ 处理结果未确认")
}

func TestHandleCardActionEventReturnsPendingThenPatchesConfirmedResult(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	sender := &deferredPatchSender{patches: make(chan string, 1), texts: make(chan string, 1)}
	adapter.sender = sender
	event := approvalCardActionEvent("allow", "允许本次", "")
	release := make(chan struct{})

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		<-release
		consumeApprovalForTest(msg)
	})
	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Content != "已受理，正在处理" {
		t.Fatalf("response=%#v, want pending callback", resp)
	}
	assertApprovalCardContent(t, resp, "已受理：允许本次")
	close(release)
	select {
	case patch := <-sender.patches:
		if !strings.Contains(patch, "✅ 已授权") {
			t.Fatalf("patch=%q, want confirmed terminal card", patch)
		}
	case <-time.After(time.Second):
		t.Fatal("confirmed approval did not patch original card")
	}
}

func TestHandleCardActionEventConcurrentApprovalDispatchesOnce(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := approvalCardActionEvent("allow", "允许本次", "")
	dispatched := make(chan platform.IncomingMessage, 16)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
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
		consumeApprovalForTest(msg)
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
		consumeApprovalForTest(msg)
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
	if second == nil || second.Toast == nil || !strings.Contains(second.Toast.Content, "任务发起人") {
		t.Fatalf("second response=%#v, want owner-only warning toast", second)
	}
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
		consumeApprovalForTest(msg)
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

func TestHandleCardActionEventRejectsApprovalWithoutOwner(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := approvalCardActionEvent("allow", "允许本次", "")
	delete(event.Event.Action.Value, "approval_owner")
	dispatched := make(chan platform.IncomingMessage, 1)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	}

	resp, err := adapter.handleCardActionEvent(context.Background(), event, dispatch)
	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Toast == nil || !strings.Contains(resp.Toast.Content, "任务发起人") {
		t.Fatalf("response=%#v, want owner-only warning toast", resp)
	}
	select {
	case msg := <-dispatched:
		t.Fatalf("ownerless approval dispatched: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleCardActionEventUsesApprovalKeyWhenMessageIDMissing(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := approvalCardActionEvent("allow", "允许本次", "")
	event.Event.Context.OpenMessageID = ""
	dispatched := make(chan platform.IncomingMessage, 2)
	dispatch := func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
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

func TestSameApprovalKeyOnDifferentCardsDispatchesIndependently(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	first := approvalCardActionEvent("allow", "允许本次", "")
	second := approvalCardActionEvent("deny", "拒绝", "")
	second.Event.Context.OpenMessageID = "om_other"
	dispatched := make(chan platform.IncomingMessage, 2)
	dispatch := func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	}

	if _, err := adapter.handleCardActionEvent(context.Background(), first, dispatch); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.handleCardActionEvent(context.Background(), second, dispatch); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-dispatched:
		case <-time.After(time.Second):
			t.Fatalf("dispatch count=%d, want 2 independent cards", i)
		}
	}
}
