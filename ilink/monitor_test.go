package ilink

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestSortMessagesForDispatchUsesSeqThenMessageID(t *testing.T) {
	messages := []WeixinMessage{
		{Seq: 2, MessageID: 20, FromUserID: "u1"},
		{Seq: 1, MessageID: 10, FromUserID: "u1"},
		{MessageID: 30, FromUserID: "u1"},
		{MessageID: 25, FromUserID: "u1"},
	}

	sortMessagesForDispatch(messages)

	var got []int64
	for _, msg := range messages {
		got = append(got, msg.MessageID)
	}
	want := []int64{10, 20, 25, 30}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("message ids = %#v, want %#v", got, want)
	}
}

func TestMonitorSerializesMessagesFromSameUser(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondHandled := make(chan struct{})
	handled := make(chan string, 2)
	monitor := &Monitor{
		queues: make(map[string]chan WeixinMessage),
		handler: func(_ context.Context, _ *Client, msg WeixinMessage) {
			text := msg.ItemList[0].TextItem.Text
			handled <- text
			if text == "first" {
				close(firstEntered)
				<-releaseFirst
				return
			}
			close(secondHandled)
		},
	}

	monitor.enqueueMessage(ctx, textMonitorMessage("u1", "first"))
	<-firstEntered
	monitor.enqueueMessage(ctx, textMonitorMessage("u1", "second"))

	select {
	case <-secondHandled:
		t.Fatal("同一用户第二条消息不应在第一条完成前处理")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case <-secondHandled:
	case <-time.After(time.Second):
		t.Fatal("同一用户第二条消息未在第一条完成后处理")
	}

	if got := []string{<-handled, <-handled}; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("handled = %#v, want first/second", got)
	}
}

func TestMonitorAllowsDifferentUsersInParallel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstEntered := make(chan struct{})
	secondHandled := make(chan struct{})
	releaseFirst := make(chan struct{})
	monitor := &Monitor{
		queues: make(map[string]chan WeixinMessage),
		handler: func(_ context.Context, _ *Client, msg WeixinMessage) {
			text := msg.ItemList[0].TextItem.Text
			if text == "first" {
				close(firstEntered)
				<-releaseFirst
				return
			}
			close(secondHandled)
		},
	}

	monitor.enqueueMessage(ctx, textMonitorMessage("u1", "first"))
	<-firstEntered
	monitor.enqueueMessage(ctx, textMonitorMessage("u2", "second"))

	select {
	case <-secondHandled:
	case <-time.After(time.Second):
		t.Fatal("不同用户消息应允许并行处理")
	}
	close(releaseFirst)
}

func TestAggregateWeixinMessagesMergesTextAndAttachments(t *testing.T) {
	first := textMonitorMessage("u1", "第一句")
	first.MessageID = 1
	second := textMonitorMessage("u1", "第二句")
	second.MessageID = 2
	second.ItemList = append(second.ItemList, MessageItem{
		Type:      ItemTypeImage,
		ImageItem: &ImageItem{URL: "https://example.com/a.png"},
	})

	got := aggregateWeixinMessages([]WeixinMessage{first, second})

	if got.MessageID != 2 {
		t.Fatalf("MessageID=%d, want latest message id 2", got.MessageID)
	}
	if len(got.ItemList) != 2 {
		t.Fatalf("items=%#v, want text plus image", got.ItemList)
	}
	if got.ItemList[0].TextItem == nil || got.ItemList[0].TextItem.Text != "第一句\n第二句" {
		t.Fatalf("merged text=%#v, want joined text", got.ItemList[0].TextItem)
	}
	if got.ItemList[1].ImageItem == nil {
		t.Fatalf("second item=%#v, want image attachment", got.ItemList[1])
	}
}

func TestRunMessageQueueFlushesBeforeCommand(t *testing.T) {
	queue := make(chan WeixinMessage, 4)
	got := make(chan WeixinMessage, 2)
	monitor := &Monitor{
		client:          &Client{},
		handler:         func(ctx context.Context, client *Client, msg WeixinMessage) { got <- msg },
		aggregateWindow: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go monitor.runMessageQueue(ctx, queue)

	queue <- textMonitorMessage("u1", "第一句")
	queue <- textMonitorMessage("u1", "/status")

	first := <-got
	second := <-got
	if first.ItemList[0].TextItem.Text != "第一句" {
		t.Fatalf("first text=%q, want aggregated text before command", first.ItemList[0].TextItem.Text)
	}
	if second.ItemList[0].TextItem.Text != "/status" {
		t.Fatalf("second text=%q, want command flushed immediately", second.ItemList[0].TextItem.Text)
	}
}

func TestCalcBackoffUsesFixedSteps(t *testing.T) {
	monitor := &Monitor{}
	want := []time.Duration{
		3 * time.Second,
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, expected := range want {
		monitor.failures = i + 1
		if got := monitor.calcBackoff(); got != expected {
			t.Fatalf("failures=%d backoff=%s, want %s", monitor.failures, got, expected)
		}
	}
}

func textMonitorMessage(userID string, text string) WeixinMessage {
	return WeixinMessage{
		FromUserID:   userID,
		MessageType:  MessageTypeUser,
		MessageState: MessageStateFinish,
		ItemList: []MessageItem{{
			Type:     ItemTypeText,
			TextItem: &TextItem{Text: text},
		}},
	}
}
