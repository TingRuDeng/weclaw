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

// TestRegularMessageDispatchWaitIsBounded 验证普通消息不会无限等待或越过仍在执行的前序操作。
func TestRegularMessageDispatchWaitIsBounded(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.dispatchWait = testDispatchWaitTimeout
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
	returned := make(chan struct{})
	go func() {
		adapter.dispatchIncomingMessage(context.Background(), msg, func(context.Context, platform.IncomingMessage, platform.Replier) {
			dispatched <- struct{}{}
		})
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(200 * time.Millisecond):
		close(blocked)
		t.Fatal("普通消息等待前序操作时未在上限内返回")
	}
	select {
	case <-dispatched:
		close(blocked)
		t.Fatal("等待超时的普通消息越过了前序操作")
	default:
	}
	select {
	case got := <-sender.texts:
		if got != "oc_1:前一项操作仍未结束，排队等待已超时，本消息未执行。请发送 /stop 或稍后重试。" {
			t.Fatalf("timeout notice=%q", got)
		}
	case <-time.After(time.Second):
		close(blocked)
		t.Fatal("排队等待超时后未发送明确反馈")
	}
	close(blocked)
	select {
	case <-dispatched:
		t.Fatal("已经超时返回的普通消息不应在之后偷偷执行")
	case <-time.After(2 * testDispatchWaitTimeout):
	}
}

func TestRegularMessageDispatchExecutionTimeoutPreservesQueue(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.dispatchWait = testDispatchWaitTimeout
	sender := &cardTimeoutNoticeSender{texts: make(chan string, 1)}
	adapter.sender = sender
	msg := platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a",
		UserID: "ou_user", ChatID: "oc_1", Text: "执行耗时操作",
	}
	blocked := make(chan struct{})
	adapter.dispatchIncomingMessage(context.Background(), msg, func(context.Context, platform.IncomingMessage, platform.Replier) {
		<-blocked
	})
	select {
	case got := <-sender.texts:
		if got != "oc_1:本消息处理超过等待上限，后台操作仍可能继续。请先检查当前状态，必要时发送 /stop。" {
			t.Fatalf("timeout notice=%q", got)
		}
	case <-time.After(time.Second):
		close(blocked)
		t.Fatal("消息处理超时后未发送明确反馈")
	}

	nextDispatched := make(chan struct{}, 1)
	go adapter.dispatchIncomingMessage(context.Background(), msg, func(context.Context, platform.IncomingMessage, platform.Replier) {
		nextDispatched <- struct{}{}
	})
	select {
	case <-nextDispatched:
		close(blocked)
		t.Fatal("超时操作尚未退出时后继消息越过队列")
	case <-time.After(2 * testDispatchWaitTimeout):
	}
	close(blocked)
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
