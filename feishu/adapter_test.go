package feishu

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// fakeWSRunner 模拟飞书长连接客户端，便于验证 Run 的生命周期控制。
type fakeWSRunner struct {
	started chan struct{}
	closed  chan struct{}
}

// Start 记录启动状态，并阻塞到 Close 被调用。
func (f *fakeWSRunner) Start(ctx context.Context) error {
	close(f.started)
	<-f.closed
	return nil
}

// Close 关闭测试长连接。
func (f *fakeWSRunner) Close() {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
}

func TestAdapterCapabilities(t *testing.T) {
	caps := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"}).Capabilities()

	if !caps.Text || !caps.Typing || !caps.Image || !caps.File || !caps.Card || !caps.Streaming || !caps.Buttons {
		t.Fatalf("capabilities=%#v, want feishu rich capabilities", caps)
	}
	if caps.LongText {
		t.Fatalf("LongText=%v, want false", caps.LongText)
	}
}

func TestAdapterRunValidatesAndStartsWS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws := &fakeWSRunner{started: make(chan struct{}), closed: make(chan struct{})}
	var validated bool
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.validate = func(ctx context.Context, creds Credentials) error {
		validated = true
		return nil
	}
	adapter.wsFactory = func(eventDispatcher *dispatcher.EventDispatcher) wsRunner {
		if eventDispatcher == nil {
			t.Fatal("event dispatcher is nil")
		}
		return ws
	}

	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})
	}()
	<-ws.started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !validated {
		t.Fatal("credentials were not validated")
	}
}

func TestAdapterRunLogsPermissionGuideAfterValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws := &fakeWSRunner{started: make(chan struct{}), closed: make(chan struct{})}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.validate = func(ctx context.Context, creds Credentials) error { return nil }
	adapter.wsFactory = func(eventDispatcher *dispatcher.EventDispatcher) wsRunner { return ws }
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})
	}()
	<-ws.started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run error: %v", err)
	}

	output := logs.String()
	if !strings.Contains(output, "https://open.feishu.cn/app/cli_a/permission") ||
		!strings.Contains(output, "im:message") ||
		!strings.Contains(output, "cardkit:card") {
		t.Fatalf("logs=%q, want permission guide", output)
	}
}

func TestAdapterRunStopsWhenValidationFails(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.validate = func(ctx context.Context, creds Credentials) error {
		return context.Canceled
	}
	adapter.wsFactory = func(eventDispatcher *dispatcher.EventDispatcher) wsRunner {
		t.Fatal("ws should not start when validation fails")
		return nil
	}

	err := adapter.Run(context.Background(), func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})

	if err == nil {
		t.Fatal("Run error=nil, want validation failure")
	}
}

func TestHandleCardActionEventDispatchesRawCommand(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_msg"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action":             cardActionChoice,
				"choice":             "1",
				"conv":               "feishu:ou_user",
				"feishu_session_key": "feishu:tenant_1:group:oc_1:om_root",
			}},
		},
	}
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" || resp.Card != nil {
		t.Fatalf("response=%#v, want success toast without card update", resp)
	}
	select {
	case msg := <-dispatched:
		if msg.RawCommand == nil || msg.RawCommand.Action != cardActionChoice || msg.RawCommand.Value["choice"] != "1" {
			t.Fatalf("msg.RawCommand=%#v, want choice command", msg.RawCommand)
		}
		if msg.Platform != platform.PlatformFeishu || msg.UserID != "ou_user" || msg.ChatID != "oc_chat" {
			t.Fatalf("msg=%#v, want feishu ids", msg)
		}
		if msg.Metadata["feishu_session_key"] != "feishu:tenant_1:group:oc_1:om_root" {
			t.Fatalf("msg.Metadata=%#v, want feishu session key", msg.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleCardActionEventRejectsUnauthorizedUser(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.SetAccessControl(platform.NewAccessControl([]string{"ou_allowed"}))
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_msg"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action": cardActionChoice,
				"choice": "1",
				"conv":   "feishu:ou_user",
			}},
		},
	}
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type == "success" {
		t.Fatalf("response=%#v, want non-success toast", resp)
	}
	select {
	case msg := <-dispatched:
		t.Fatalf("unexpected dispatch: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleCardActionEventUpdatesMappedTaskCard(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card", resp)
	}
	assertApprovalCardContent(t, resp, "✅ 已收纳到任务卡片")
	assertApprovalCardNotContains(t, resp, "command: date")
	if cardKit.updateCountFor("card-task-1") != 1 {
		t.Fatalf("updated card ids=%#v, want task card update", cardKit.updateCardIDs)
	}
	select {
	case msg := <-dispatched:
		if msg.RawCommand.Value["approval_key"] != "approval-key-1" {
			t.Fatalf("raw command=%#v, want approval key passthrough", msg.RawCommand.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleCardActionEventAppendsApprovalToTaskCardState(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	adapter.taskCards.record("card-task-1", cardOptions{
		Status:  cardStatusThinking,
		Title:   "Codex",
		Content: "正在分析任务",
	})
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card", resp)
	}
	assertApprovalCardContent(t, resp, "✅ 已收纳到任务卡片")
	assertApprovalCardNotContains(t, resp, "command: date")
	if cardKit.updateCountFor("card-task-1") != 1 {
		t.Fatalf("updated card ids=%#v, want task card update", cardKit.updateCardIDs)
	}
	card := decodeCardJSON(t, cardKit.updateCards[0])
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	main := elements[1].(map[string]any)
	approval := elements[2].(map[string]any)
	if main["content"] != "正在分析任务" {
		t.Fatalf("main content=%#v, want preserved task content", main["content"])
	}
	if !strings.Contains(approval["content"].(string), "允许本次") {
		t.Fatalf("approval content=%q, want approval label", approval["content"])
	}
	select {
	case msg := <-dispatched:
		if msg.RawCommand.Value["task_card_id"] != "card-task-1" {
			t.Fatalf("raw command=%#v, want task card passthrough", msg.RawCommand.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleCardActionEventIgnoresTaskCardUpdateFailure(t *testing.T) {
	cardKit := &fakeCardKitClient{updateErrors: []error{context.Canceled}}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card despite task card failure", resp)
	}
	assertApprovalCardContent(t, resp, "✅ 已授权", "允许本次", "command: date")
	assertApprovalCardNotContains(t, resp, "已收纳到任务卡片")
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

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

func approvalCardActionEvent(choice string, label string, taskCardID string) *callback.CardActionTriggerEvent {
	value := map[string]interface{}{
		"action":       cardActionChoice,
		"choice":       choice,
		"kind":         cardKindApproval,
		"label":        label,
		"summary":      "command: date",
		"approval_key": "approval-key-1",
	}
	if taskCardID != "" {
		value["task_card_id"] = taskCardID
	}
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_msg"},
			Action:   &callback.CallBackAction{Value: value},
		},
	}
}

func assertApprovalCardContent(t *testing.T, resp *callback.CardActionTriggerResponse, wants ...string) {
	t.Helper()
	content := approvalCardContentForTest(t, resp)
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Fatalf("content=%q, want %q", content, want)
		}
	}
}

func assertApprovalCardNotContains(t *testing.T, resp *callback.CardActionTriggerResponse, forbidden ...string) {
	t.Helper()
	content := approvalCardContentForTest(t, resp)
	for _, value := range forbidden {
		if strings.Contains(content, value) {
			t.Fatalf("content=%q, should not contain %q", content, value)
		}
	}
}

func approvalCardContentForTest(t *testing.T, resp *callback.CardActionTriggerResponse) string {
	t.Helper()
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card", resp)
	}
	card := resp.Card.Data.(map[string]any)
	body := card["body"].(map[string]any)
	content := body["elements"].([]map[string]any)[0]["content"].(string)
	return content
}
