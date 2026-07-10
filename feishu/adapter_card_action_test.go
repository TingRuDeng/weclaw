package feishu

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestEventDispatcherIgnoresMessageReadEvent(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	dispatcher := adapter.newEventDispatcher(func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		t.Fatalf("message read event must not dispatch incoming message, got %#v", msg)
	})

	payload := []byte(`{
		"schema":"2.0",
		"header":{
			"event_id":"evt_1",
			"event_type":"im.message.message_read_v1",
			"create_time":"1720000000000",
			"token":"",
			"app_id":"cli_a",
			"tenant_key":"tenant_1"
		},
		"event":{
			"message_id_list":["om_1"],
			"reader":{
				"reader_id":{"open_id":"ou_user"},
				"read_time":"1720000000000",
				"tenant_key":"tenant_1"
			}
		}
	}`)

	if _, err := dispatcher.Do(context.Background(), payload); err != nil {
		t.Fatalf("message read event should be ignored without error, got %v", err)
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
				"label":              "账本 App 开发",
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
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response=%#v, want success toast", resp)
	}
	assertSelectedChoiceCard(t, resp.Card, "账本 App 开发")
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

func TestHandleCardActionEventGroupReplyUsesFreshMessage(t *testing.T) {
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
				"feishu_session_key": "feishu:tenant_1:group:oc_chat",
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
	if len(sender.replyTexts) != 0 {
		t.Fatalf("replyTexts=%#v, want no card message thread reply", sender.replyTexts)
	}
	if len(sender.texts) != 1 || sender.texts[0] != "oc_chat:已切换" {
		t.Fatalf("texts=%#v, want fresh group card callback message", sender.texts)
	}
}

func TestHandleCardActionEventDMReplyUsesFreshMessage(t *testing.T) {
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
				"feishu_session_key": "feishu:tenant_1:dm:oc_chat:ou_user",
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
	if len(sender.replyTexts) != 0 {
		t.Fatalf("replyTexts=%#v, want no DM reply thread", sender.replyTexts)
	}
	if len(sender.texts) != 1 || sender.texts[0] != "oc_chat:已切换" {
		t.Fatalf("texts=%#v, want fresh DM card callback message", sender.texts)
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
	if resp.Card != nil {
		t.Fatalf("response=%#v, unauthorized action must not update card", resp)
	}
	select {
	case msg := <-dispatched:
		t.Fatalf("unexpected dispatch: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleCardActionEventAllowsCachedUnionIDAlias(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = &fakeResourceDownloader{}
	adapter.SetAccessControl(platform.NewAccessControl([]string{"on_same_person"}))
	message := newMessageEvent("p2p", "text", `{"text":"hello"}`)
	message.Event.Sender.SenderId.UnionId = stringPtr("on_same_person")
	if err := adapter.handleMessageEvent(context.Background(), message, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {}); err != nil {
		t.Fatalf("handleMessageEvent error: %v", err)
	}
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
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response=%#v, want success toast", resp)
	}
	select {
	case msg := <-dispatched:
		if !containsString(msg.UserAliases, "on_same_person") {
			t.Fatalf("aliases=%#v, want cached union_id", msg.UserAliases)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleCardActionEventAllowsOperatorUserIDAlias(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.SetAccessControl(platform.NewAccessControl([]string{"user_1"}))
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user", UserID: stringPtr("user_1")},
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
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response=%#v, want success toast", resp)
	}
	select {
	case msg := <-dispatched:
		if !containsString(msg.UserAliases, "user_1") {
			t.Fatalf("aliases=%#v, want callback user_id", msg.UserAliases)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}
