package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

type permanentFakeResourceError struct{ error }

func (permanentFakeResourceError) Permanent() bool { return true }

type failingDirectTextSender struct {
	fakeMessageSender
	err error
}

func (s *failingDirectTextSender) SendText(ctx context.Context, openID string, text string) error {
	s.fakeMessageSender.SendText(ctx, openID, text)
	return s.err
}

func TestHandleMessageEventRetriesTransientAttachmentFailure(t *testing.T) {
	downloader := &fakeResourceDownloader{errors: []error{errors.New("temporary network failure"), nil}}
	sender := &fakeMessageSender{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = downloader
	adapter.sender = sender
	event := newMessageEvent("p2p", "image", `{"image_key":"img_1"}`)
	dispatches := 0
	dispatch := func(context.Context, platform.IncomingMessage, platform.Replier) { dispatches++ }

	if err := adapter.handleMessageEvent(context.Background(), event, dispatch); err == nil {
		t.Fatal("临时附件错误应返回给飞书触发重试")
	}
	assertAttachmentFailureReply(t, sender)
	if err := adapter.handleMessageEvent(context.Background(), event, dispatch); err != nil {
		t.Fatalf("retry error: %v", err)
	}
	if dispatches != 1 || len(downloader.seen) != 2 {
		t.Fatalf("dispatches=%d downloads=%d", dispatches, len(downloader.seen))
	}
}

func TestHandleMessageEventConsumesPermanentAttachmentFailure(t *testing.T) {
	downloader := &fakeResourceDownloader{errors: []error{
		permanentFakeResourceError{error: errors.New("resource too large")},
	}}
	sender := &fakeMessageSender{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = downloader
	adapter.sender = sender
	event := newMessageEvent("p2p", "image", `{"image_key":"img_1"}`)
	dispatches := 0
	dispatch := func(context.Context, platform.IncomingMessage, platform.Replier) { dispatches++ }

	if err := adapter.handleMessageEvent(context.Background(), event, dispatch); err != nil {
		t.Fatalf("permanent error should be consumed: %v", err)
	}
	assertAttachmentFailureReply(t, sender)
	if err := adapter.handleMessageEvent(context.Background(), event, dispatch); err != nil {
		t.Fatalf("duplicate event error: %v", err)
	}
	if dispatches != 0 || len(downloader.seen) != 1 {
		t.Fatalf("dispatches=%d downloads=%d", dispatches, len(downloader.seen))
	}
}

func TestHandleMessageEventConsumesAttachmentPermissionFailureInParentChat(t *testing.T) {
	downloader := &fakeResourceDownloader{errors: []error{
		newFeishuResourceAPIError("cli_a", "img_1", 99991672, "missing message scope"),
	}}
	sender := &fakeMessageSender{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = downloader
	adapter.sender = sender
	event := newMessageEvent("p2p", "image", `{"image_key":"img_1"}`)
	dispatches := 0
	dispatch := func(context.Context, platform.IncomingMessage, platform.Replier) { dispatches++ }

	if err := adapter.handleMessageEvent(context.Background(), event, dispatch); err != nil {
		t.Fatalf("permission error should be consumed: %v", err)
	}
	if len(sender.replyTexts) != 0 {
		t.Fatalf("replyTexts=%#v, permission guide must not create a child thread", sender.replyTexts)
	}
	if len(sender.texts) != 1 || !strings.Contains(sender.texts[0], "im:message:readonly") {
		t.Fatalf("texts=%#v, want direct resource permission guide", sender.texts)
	}
	if err := adapter.handleMessageEvent(context.Background(), event, dispatch); err != nil {
		t.Fatalf("duplicate event error: %v", err)
	}
	if dispatches != 0 || len(downloader.seen) != 1 {
		t.Fatalf("dispatches=%d downloads=%d", dispatches, len(downloader.seen))
	}
}

func TestHandleMessageEventConsumesPermissionFailureWhenNoticeFails(t *testing.T) {
	downloader := &fakeResourceDownloader{errors: []error{
		newFeishuResourceAPIError("cli_a", "img_1", 99991672, "missing message scope"),
	}}
	sender := &failingDirectTextSender{err: errors.New("send notice failed")}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = downloader
	adapter.sender = sender
	event := newMessageEvent("p2p", "image", `{"image_key":"img_1"}`)
	dispatch := func(context.Context, platform.IncomingMessage, platform.Replier) {}

	if err := adapter.handleMessageEvent(context.Background(), event, dispatch); err != nil {
		t.Fatalf("terminal permission error should be consumed even when notice fails: %v", err)
	}
	if err := adapter.handleMessageEvent(context.Background(), event, dispatch); err != nil {
		t.Fatalf("duplicate event error: %v", err)
	}
	if len(downloader.seen) != 1 {
		t.Fatalf("downloads=%d, permission event should remain deduplicated", len(downloader.seen))
	}
}

func assertAttachmentFailureReply(t *testing.T, sender *fakeMessageSender) {
	t.Helper()
	if len(sender.texts) != 0 {
		t.Fatalf("texts=%#v, failure must reply to source message", sender.texts)
	}
	if len(sender.replyTexts) != 1 || !strings.Contains(sender.replyTexts[0], "附件获取失败") {
		t.Fatalf("replyTexts=%#v, want attachment failure notice", sender.replyTexts)
	}
}
