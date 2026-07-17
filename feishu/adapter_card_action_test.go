package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
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
				"action":              cardActionChoice,
				"choice":              "1",
				"label":               "账本 App 开发",
				"conv":                "feishu:ou_user",
				"feishu_session_key":  "feishu:tenant_1:group:oc_1:om_root",
				"navigation_snapshot": "snapshot-1",
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
	if resp.Toast.Content != "已受理，正在处理" {
		t.Fatalf("toast=%q, want pending status", resp.Toast.Content)
	}
	assertPendingChoiceCard(t, resp.Card, "账本 App 开发", "正在处理")
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
		if msg.RawCommand.Value["navigation_snapshot"] != "snapshot-1" {
			t.Fatalf("msg.RawCommand=%#v, want navigation snapshot", msg.RawCommand)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleCardActionEventUsesEventIDForRepeatedNavigation(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	messageIDs := make([]string, 0, 3)
	dispatch := func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		messageIDs = append(messageIDs, msg.MessageID)
	}
	for _, eventID := range []string{"evt_page_2_first", "evt_page_2_again", "evt_page_2_again"} {
		event := &callback.CardActionTriggerEvent{
			EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: eventID}},
			Event: &callback.CardActionTriggerRequest{
				Operator: &callback.Operator{OpenID: "ou_user"},
				Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_card"},
				Action: &callback.CallBackAction{Value: map[string]interface{}{
					"action": cardActionChoice,
					"choice": "/cx page workspaces 2",
					"label":  "下一页 →",
				}},
			},
		}
		if _, err := adapter.handleCardActionEvent(context.Background(), event, dispatch); err != nil {
			t.Fatal(err)
		}
	}
	if len(messageIDs) != 3 || messageIDs[0] == messageIDs[1] || messageIDs[1] != messageIDs[2] {
		t.Fatalf("messageIDs=%#v，不同点击必须区分，同一事件重投必须保持幂等", messageIDs)
	}
}

func TestHandleCardActionEventFallsBackToCardRevision(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	messageIDs := make([]string, 0, 2)
	for _, revision := range []string{"revision-page-1", "revision-page-1-again"} {
		event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_card"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action": cardActionChoice, "choice": "/cx page workspaces 2", cardRevisionValueKey: revision,
			}},
		}}
		if _, err := adapter.handleCardActionEvent(context.Background(), event, func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
			messageIDs = append(messageIDs, msg.MessageID)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(messageIDs) != 2 || messageIDs[0] == messageIDs[1] {
		t.Fatalf("messageIDs=%#v，不带事件 ID 时必须按卡片 revision 区分后续点击", messageIDs)
	}
}

func TestHandleCardActionEventGroupResultUpdatesOriginalCard(t *testing.T) {
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
	assertInlineCardContent(t, resp.Card, "已切换")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
	if len(sender.replyTexts) != 0 {
		t.Fatalf("replyTexts=%#v, want no card message thread reply", sender.replyTexts)
	}
	if len(sender.texts) != 0 {
		t.Fatalf("texts=%#v, quick result should update original group card", sender.texts)
	}
}

func TestHandleCardActionEventDMResultUpdatesOriginalCard(t *testing.T) {
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
	assertInlineCardContent(t, resp.Card, "已切换")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
	if len(sender.replyTexts) != 0 {
		t.Fatalf("replyTexts=%#v, want no DM reply thread", sender.replyTexts)
	}
	if len(sender.texts) != 0 {
		t.Fatalf("texts=%#v, quick result should update original DM card", sender.texts)
	}
}

func TestHandleCardActionEventInlineChoiceCardReplacesOriginal(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	sender := &fakeMessageSender{}
	adapter.sender = sender
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_card"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			"action": cardActionChoice, "choice": "/help codex", "label": "Codex",
		}},
	}}

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, _ platform.IncomingMessage, reply platform.Replier) {
		if err := reply.AskChoices(ctx, "Codex 帮助", []platform.Choice{{ID: "/cx ls", Label: "工作空间"}}); err != nil {
			t.Fatalf("AskChoices error: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertInlineCardContent(t, resp.Card, "Codex 帮助")
	data, _ := json.Marshal(resp.Card.Data)
	if !strings.Contains(string(data), `"choice":"/cx ls"`) {
		t.Fatalf("card=%s，期望原卡直接替换为下一层按钮", data)
	}
	if len(sender.texts) != 0 || len(sender.cards) != 0 {
		t.Fatalf("sender=%#v，原卡更新不应再发新消息", sender)
	}
}

func TestHandleCardActionEventInlineUsesLastSynchronousResult(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	sender := &fakeMessageSender{}
	adapter.sender = sender
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_card"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			"action": cardActionChoice, "choice": "/status", "label": "状态",
		}},
	}}

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, _ platform.IncomingMessage, reply platform.Replier) {
		_ = reply.SendText(ctx, "中间结果")
		_ = reply.SendText(ctx, "最终结果")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertInlineCardContent(t, resp.Card, "最终结果")
	data, _ := json.Marshal(resp.Card.Data)
	if strings.Contains(string(data), "中间结果") || len(sender.texts) != 0 {
		t.Fatalf("card=%s texts=%#v，原卡应只展示同步命令最终结果", data, sender.texts)
	}
}

func assertInlineCardContent(t *testing.T, card *callback.Card, want string) {
	t.Helper()
	if card == nil {
		t.Fatal("response card is nil")
	}
	data, err := json.Marshal(card.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("card=%s，期望包含 %q", data, want)
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
