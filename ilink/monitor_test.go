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

func textMonitorMessage(userID string, text string) WeixinMessage {
	return WeixinMessage{
		FromUserID: userID,
		ItemList: []MessageItem{{
			Type:     ItemTypeText,
			TextItem: &TextItem{Text: text},
		}},
	}
}
