package wechat

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestAdapterIdentityAndCapabilities(t *testing.T) {
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})

	if adapter.Name() != platform.PlatformWeChat {
		t.Fatalf("Name=%q, want wechat", adapter.Name())
	}
	if adapter.AccountID() != "bot-1" {
		t.Fatalf("AccountID=%q, want bot-1", adapter.AccountID())
	}
	caps := adapter.Capabilities()
	if !caps.Text || !caps.Typing || !caps.Image || !caps.File || !caps.LongText {
		t.Fatalf("wechat capabilities missing expected true values: %#v", caps)
	}
	if caps.Card || caps.Streaming || caps.Buttons {
		t.Fatalf("wechat capabilities should not enable card/streaming/buttons: %#v", caps)
	}
}

type fakeWechatMonitor struct {
	started      chan struct{}
	stopped      chan struct{}
	lastActivity time.Time
}

func (f *fakeWechatMonitor) Run(ctx context.Context) error {
	close(f.started)
	<-ctx.Done()
	close(f.stopped)
	return ctx.Err()
}

func (f *fakeWechatMonitor) LastActivity() time.Time {
	return f.lastActivity
}

func (f *fakeWechatMonitor) SetAggregationWindow(window time.Duration) {
}

func TestAdapterWatchdogCancelsIdleMonitor(t *testing.T) {
	monitor := &fakeWechatMonitor{
		started:      make(chan struct{}),
		stopped:      make(chan struct{}),
		lastActivity: time.Now().Add(-time.Minute),
	}
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	adapter.newMonitor = func(client *ilink.Client, handler ilink.MessageHandler) (monitorRunner, error) {
		return monitor, nil
	}
	adapter.watchdogInterval = time.Millisecond
	adapter.watchdogMaxIdle = time.Millisecond

	errCh := make(chan error, 1)
	go func() {
		errCh <- adapter.Run(context.Background(), func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})
	}()
	<-monitor.started
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("Run error=%v, want watchdog context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not cancel idle monitor")
	}
}

func TestAdapterRunReturnsNilOnOuterCancel(t *testing.T) {
	monitor := &fakeWechatMonitor{
		started:      make(chan struct{}),
		stopped:      make(chan struct{}),
		lastActivity: time.Now(),
	}
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	adapter.newMonitor = func(client *ilink.Client, handler ilink.MessageHandler) (monitorRunner, error) {
		return monitor, nil
	}
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- adapter.Run(ctx, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {})
	}()
	<-monitor.started
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run error=%v, want nil on outer cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after outer cancel")
	}
}

func TestAdapterSkipsSelfEchoMessage(t *testing.T) {
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot-1"})
	dispatched := 0
	handler := adapter.handleWeixinMessage(func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched++
	})

	handler(context.Background(), client, ilink.WeixinMessage{
		FromUserID:   "user-1",
		ToUserID:     "bot-1",
		ClientID:     "weclaw:echo",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
	})
	handler(context.Background(), client, ilink.WeixinMessage{
		FromUserID:   "user-1",
		ToUserID:     "bot-1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
	})

	if dispatched != 1 {
		t.Fatalf("dispatched=%d, want only normal user message", dispatched)
	}
}
