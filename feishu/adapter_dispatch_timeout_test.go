package feishu

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

const testDispatchWaitTimeout = 20 * time.Millisecond
const testDispatchNoticeDelay = 20 * time.Millisecond

// TestMessageDispatchContinuesAfterPreviousTicketTimeout 验证同窗口前序任务阻塞时后续控制消息仍能分发。
func TestMessageDispatchContinuesAfterPreviousTicketTimeout(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.dispatchWait = testDispatchWaitTimeout
	msg := platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a",
		UserID: "ou_user", ChatID: "oc_1", Text: "/stop",
	}
	first := adapter.dispatches.reserve(feishuDispatchKey(msg))
	blocked := make(chan struct{})
	go first.run(context.Background(), func() { <-blocked })
	dispatched := make(chan struct{}, 1)

	go adapter.dispatchIncomingMessage(context.Background(), msg, func(context.Context, platform.IncomingMessage, platform.Replier) {
		dispatched <- struct{}{}
	})
	select {
	case <-dispatched:
	case <-time.After(200 * time.Millisecond):
		close(blocked)
		t.Fatal("前序任务阻塞后 /stop 未在等待上限内继续分发")
	}
	close(blocked)
}

// TestRegularMessageDispatchDoesNotBypassPreviousTicket 验证普通消息不会越过仍在执行的前序操作。
func TestRegularMessageDispatchDoesNotBypassPreviousTicket(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.dispatchWait = testDispatchWaitTimeout
	msg := platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a",
		UserID: "ou_user", ChatID: "oc_1", Text: "继续处理",
	}
	first := adapter.dispatches.reserve(feishuDispatchKey(msg))
	blocked := make(chan struct{})
	go first.run(context.Background(), func() { <-blocked })
	dispatched := make(chan struct{}, 1)
	go adapter.dispatchIncomingMessage(context.Background(), msg, func(context.Context, platform.IncomingMessage, platform.Replier) {
		dispatched <- struct{}{}
	})

	select {
	case <-dispatched:
		close(blocked)
		t.Fatal("普通消息越过了仍在执行的前序操作")
	case <-time.After(2 * testDispatchWaitTimeout):
	}
	close(blocked)
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("前序操作结束后普通消息仍未分发")
	}
}

// TestRegularMessageDispatchSendsSingleQueueNotice 验证普通消息长时间等待时只提示一次且不会乱序执行。
func TestRegularMessageDispatchSendsSingleQueueNotice(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.dispatchNoticeDelay = testDispatchNoticeDelay
	sender := &cardTimeoutNoticeSender{texts: make(chan string, 2)}
	adapter.sender = sender
	msg := platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a",
		UserID: "ou_user", ChatID: "oc_1", Text: "继续处理",
	}
	first := adapter.dispatches.reserve(feishuDispatchKey(msg))
	blocked := make(chan struct{})
	go first.run(context.Background(), func() { <-blocked })
	dispatched := make(chan struct{}, 1)
	go adapter.dispatchIncomingMessage(context.Background(), msg, func(context.Context, platform.IncomingMessage, platform.Replier) {
		dispatched <- struct{}{}
	})

	select {
	case got := <-sender.texts:
		if got != "oc_1:前一项操作仍在处理，本消息已排队，完成后将自动执行。" {
			t.Fatalf("queue notice=%q", got)
		}
	case <-time.After(time.Second):
		close(blocked)
		t.Fatal("普通消息等待超过阈值后未发送排队提示")
	}
	select {
	case <-dispatched:
		close(blocked)
		t.Fatal("普通消息在提示后越过了前序操作")
	case <-time.After(2 * testDispatchNoticeDelay):
	}
	select {
	case duplicate := <-sender.texts:
		close(blocked)
		t.Fatalf("排队提示重复发送: %q", duplicate)
	default:
	}
	close(blocked)
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("前序操作结束后普通消息仍未执行")
	}
}

// TestRegularMessageDispatchSkipsQueueNoticeForShortWait 验证短暂排队不会产生多余提示。
func TestRegularMessageDispatchSkipsQueueNoticeForShortWait(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.dispatchNoticeDelay = 4 * testDispatchNoticeDelay
	sender := &cardTimeoutNoticeSender{texts: make(chan string, 1)}
	adapter.sender = sender
	msg := platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a",
		UserID: "ou_user", ChatID: "oc_1", Text: "继续处理",
	}
	first := adapter.dispatches.reserve(feishuDispatchKey(msg))
	blocked := make(chan struct{})
	go first.run(context.Background(), func() { <-blocked })
	dispatched := make(chan struct{}, 1)
	go adapter.dispatchIncomingMessage(context.Background(), msg, func(context.Context, platform.IncomingMessage, platform.Replier) {
		dispatched <- struct{}{}
	})
	close(blocked)

	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("短暂等待结束后普通消息未执行")
	}
	select {
	case got := <-sender.texts:
		t.Fatalf("短暂等待不应发送排队提示: %q", got)
	case <-time.After(2 * adapter.dispatchNoticeDelay):
	}
}
