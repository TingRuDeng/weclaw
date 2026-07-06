package feishu

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestHandleMessageEventReplyUsesSourceMessage(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	sender := &fakeMessageSender{}
	adapter.sender = sender
	adapter.downloader = &fakeResourceDownloader{}
	event := newMessageEvent("group", "text", `{"text":"<at user_id=\"cli_a\">bot</at> hello"}`)
	event.Event.Message.RootId = stringPtr("om_root")
	event.Event.Message.Mentions = []*larkim.MentionEvent{newMention("cli_a")}

	err := adapter.handleMessageEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		if err := reply.SendText(ctx, "收到"); err != nil {
			t.Fatalf("SendText error: %v", err)
		}
	})

	if err != nil {
		t.Fatalf("handleMessageEvent error: %v", err)
	}
	if len(sender.texts) != 0 {
		t.Fatalf("texts=%#v, want no fresh group message", sender.texts)
	}
	if len(sender.replyTexts) != 1 || sender.replyTexts[0] != "om_1:收到" {
		t.Fatalf("replyTexts=%#v, want reply to source message", sender.replyTexts)
	}
}

func TestHandleMessageEventDMReplyUsesFreshMessage(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	sender := &fakeMessageSender{}
	adapter.sender = sender
	adapter.downloader = &fakeResourceDownloader{}
	event := newMessageEvent("p2p", "text", `{"text":"hello"}`)

	err := adapter.handleMessageEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		if err := reply.SendText(ctx, "收到"); err != nil {
			t.Fatalf("SendText error: %v", err)
		}
	})

	if err != nil {
		t.Fatalf("handleMessageEvent error: %v", err)
	}
	if len(sender.replyTexts) != 0 {
		t.Fatalf("replyTexts=%#v, want no DM reply thread", sender.replyTexts)
	}
	if len(sender.texts) != 1 || sender.texts[0] != "oc_1:收到" {
		t.Fatalf("texts=%#v, want fresh DM message", sender.texts)
	}
}

func TestHandleMessageEventDMNewThreadReplyUsesSourceMessage(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	sender := &fakeMessageSender{}
	adapter.sender = sender
	adapter.downloader = &fakeResourceDownloader{}
	event := newMessageEvent("p2p", "text", `{"text":"/cx new-thread"}`)

	err := adapter.handleMessageEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		if err := reply.SendText(ctx, "已开启"); err != nil {
			t.Fatalf("SendText error: %v", err)
		}
	})

	if err != nil {
		t.Fatalf("handleMessageEvent error: %v", err)
	}
	if len(sender.texts) != 0 {
		t.Fatalf("texts=%#v, want no fresh DM message", sender.texts)
	}
	if len(sender.replyTexts) != 1 || sender.replyTexts[0] != "om_1:已开启" {
		t.Fatalf("replyTexts=%#v, want reply to DM command message", sender.replyTexts)
	}
}

func TestHandleMessageEventDMThreadReplyUsesSourceMessage(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	sender := &fakeMessageSender{}
	adapter.sender = sender
	adapter.downloader = &fakeResourceDownloader{}
	event := newMessageEvent("p2p", "text", `{"text":"继续"}`)
	event.Event.Message.MessageId = stringPtr("om_2")
	event.Event.Message.RootId = stringPtr("om_1")

	err := adapter.handleMessageEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		if err := reply.SendText(ctx, "收到"); err != nil {
			t.Fatalf("SendText error: %v", err)
		}
	})

	if err != nil {
		t.Fatalf("handleMessageEvent error: %v", err)
	}
	if len(sender.texts) != 0 {
		t.Fatalf("texts=%#v, want no fresh DM message", sender.texts)
	}
	if len(sender.replyTexts) != 1 || sender.replyTexts[0] != "om_2:收到" {
		t.Fatalf("replyTexts=%#v, want reply to DM thread message", sender.replyTexts)
	}
}
