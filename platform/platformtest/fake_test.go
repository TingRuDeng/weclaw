package platformtest

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestFakeReplierRecordsText(t *testing.T) {
	reply := NewReplier(platform.Capabilities{Text: true})

	if err := reply.SendText(context.Background(), "hello"); err != nil {
		t.Fatalf("SendText error: %v", err)
	}
	if len(reply.Texts) != 1 || reply.Texts[0] != "hello" {
		t.Fatalf("texts=%#v, want hello", reply.Texts)
	}
}

func TestFakeStreamRecordsLifecycle(t *testing.T) {
	stream := &Stream{}

	if err := stream.Update(context.Background(), "part"); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if err := stream.Complete(context.Background(), "done"); err != nil {
		t.Fatalf("Complete error: %v", err)
	}

	if len(stream.Updates) != 1 || stream.Updates[0] != "part" || stream.Completed != "done" {
		t.Fatalf("stream=%#v", stream)
	}
}

func TestFakePlatformDispatchesMessages(t *testing.T) {
	reply := NewReplier(platform.Capabilities{Text: true})
	p := NewPlatform("acct-1", []platform.IncomingMessage{{
		Platform: platform.PlatformWeChat,
		UserID:   "user-1",
		Text:     "hi",
	}}, reply)
	var got []platform.IncomingMessage

	err := p.Run(context.Background(), func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		got = append(got, msg)
	})

	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(got) != 1 || got[0].Text != "hi" {
		t.Fatalf("messages=%#v, want one hi", got)
	}
}
