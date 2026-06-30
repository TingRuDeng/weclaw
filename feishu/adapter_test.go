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
	if cardKit.updateCountFor("card-task-1") != 1 {
		t.Fatalf("updated card ids=%#v, want task card update", cardKit.updateCardIDs)
	}
	select {
	case <-dispatched:
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
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
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
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card", resp)
	}
	card := resp.Card.Data.(map[string]any)
	body := card["body"].(map[string]any)
	content := body["elements"].([]map[string]any)[0]["content"].(string)
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Fatalf("content=%q, want %q", content, want)
		}
	}
}
