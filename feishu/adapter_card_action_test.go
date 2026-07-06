package feishu

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

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

func TestHandleCardActionEventReplyUsesCardMessage(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	sender := &fakeMessageSender{}
	adapter.sender = sender
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_card"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action":             cardActionChoice,
				"choice":             "/cx status",
				"conv":               "feishu:ou_user",
				"feishu_session_key": "feishu:tenant_1:group:oc_chat:om_root",
			}},
		},
	}
	done := make(chan struct{}, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		if err := reply.SendText(ctx, "已切换"); err != nil {
			t.Fatalf("SendText error: %v", err)
		}
		done <- struct{}{}
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response=%#v, want success toast", resp)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
	if len(sender.texts) != 0 {
		t.Fatalf("texts=%#v, want no fresh card callback reply", sender.texts)
	}
	if len(sender.replyTexts) != 1 || sender.replyTexts[0] != "om_card:已切换" {
		t.Fatalf("replyTexts=%#v, want reply to card message", sender.replyTexts)
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
