package feishu

import (
	"context"
	"fmt"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type dedupTestEventOptions struct {
	MessageID  string
	EventID    string
	ThreadID   string
	CreateTime string
	Text       string
	ChatType   string
}

func TestHandleMessageEventDedupesSameFeishuMessageID(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_same", EventID: "evt_1", ThreadID: "omt_thread",
		CreateTime: "1719730000000", Text: "@_user_1 hello",
	})

	dispatches := dispatchFeishuEvents(adapter, event, event)

	if dispatches != 1 {
		t.Fatalf("dispatches=%d, want same message_id dispatched once", dispatches)
	}
}

func TestHandleMessageEventDedupesSameThreadContentWithDifferentMessageID(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	first := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_a", EventID: "evt_a", ThreadID: "omt_thread",
		CreateTime: "1719730000000", Text: "@_user_1 A 线程的水果是什么？",
	})
	second := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_b", EventID: "evt_b", ThreadID: "omt_thread",
		CreateTime: "1719730000000", Text: "@_user_1 A 线程的水果是什么？",
	})

	dispatches := dispatchFeishuEvents(adapter, first, second)

	if dispatches != 1 {
		t.Fatalf("dispatches=%d, want same thread/content duplicate dispatched once", dispatches)
	}
}

func TestHandleMessageEventDispatchesDifferentFeishuMessageID(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	first := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_a", EventID: "evt_a", ThreadID: "omt_thread",
		CreateTime: "1719730000000", Text: "@_user_1 hello A",
	})
	second := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_b", EventID: "evt_b", ThreadID: "omt_thread",
		CreateTime: "1719730001000", Text: "@_user_1 hello B",
	})

	dispatches := dispatchFeishuEvents(adapter, first, second)

	if dispatches != 2 {
		t.Fatalf("dispatches=%d, want different message_id dispatched twice", dispatches)
	}
}

func TestHandleMessageEventDoesNotDedupDifferentThreads(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	first := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_a", EventID: "evt_a", ThreadID: "omt_thread_a",
		CreateTime: "1719730000000", Text: "@_user_1 同一个问题",
	})
	second := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_b", EventID: "evt_b", ThreadID: "omt_thread_b",
		CreateTime: "1719730000000", Text: "@_user_1 同一个问题",
	})

	dispatches := dispatchFeishuEvents(adapter, first, second)

	if dispatches != 2 {
		t.Fatalf("dispatches=%d, want different threads dispatched twice", dispatches)
	}
}

func TestHandleMessageEventDoesNotContentDedupDM(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	first := newDMEvent(dedupTestEventOptions{
		MessageID: "om_dm_a", EventID: "evt_dm_a",
		CreateTime: "1719730000000", Text: "hello",
	})
	second := newDMEvent(dedupTestEventOptions{
		MessageID: "om_dm_b", EventID: "evt_dm_b",
		CreateTime: "1719730000000", Text: "hello",
	})

	dispatches := dispatchFeishuEvents(adapter, first, second)

	if dispatches != 2 {
		t.Fatalf("dispatches=%d, want DM messages with different IDs dispatched twice", dispatches)
	}
}

// dispatchFeishuEvents 统计事件进入 adapter 后实际分发到 messaging 的次数。
func dispatchFeishuEvents(adapter *Adapter, events ...*larkim.P2MessageReceiveV1) int {
	dispatches := 0
	for _, event := range events {
		_ = adapter.handleMessageEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
			dispatches++
		})
	}
	return dispatches
}

// newDispatchableGroupEvent 构造默认可触发 agent 的飞书群聊事件。
func newDispatchableGroupEvent(opts dedupTestEventOptions) *larkim.P2MessageReceiveV1 {
	event := newMessageEvent("group", "text", fmt.Sprintf(`{"text":%q}`, opts.Text))
	setMessageIdentity(event, opts)
	event.Event.Message.ThreadId = stringPtr(opts.ThreadID)
	event.Event.Message.Mentions = []*larkim.MentionEvent{newTypedMention("ou_bot_open_id", "app")}
	return event
}

// newDMEvent 构造飞书私聊事件，验证群聊内容指纹不会影响 DM。
func newDMEvent(opts dedupTestEventOptions) *larkim.P2MessageReceiveV1 {
	event := newMessageEvent("p2p", "text", fmt.Sprintf(`{"text":%q}`, opts.Text))
	setMessageIdentity(event, opts)
	return event
}

// setMessageIdentity 设置飞书事件和消息 ID，便于覆盖去重边界。
func setMessageIdentity(event *larkim.P2MessageReceiveV1, opts dedupTestEventOptions) {
	event.EventV2Base.Header.EventID = opts.EventID
	event.Event.Message.MessageId = stringPtr(opts.MessageID)
	event.Event.Message.CreateTime = stringPtr(opts.CreateTime)
}
