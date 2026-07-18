package feishu

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

type cardTimeoutNoticeSender struct {
	fakeMessageSender
	texts chan string
}

type deferredPatchSender struct {
	fakeMessageSender
	patches chan string
	texts   chan string
}

func (s *deferredPatchSender) PatchCard(_ context.Context, messageID string, cardJSON string) error {
	s.patches <- messageID + ":" + cardJSON
	return nil
}

func (s *deferredPatchSender) SendText(_ context.Context, openID string, text string) error {
	s.texts <- openID + ":" + text
	return nil
}

// SendText 通过通道记录超时通知，避免异步测试读取共享切片产生竞态。
func (s *cardTimeoutNoticeSender) SendText(_ context.Context, openID string, text string) error {
	s.texts <- openID + ":" + text
	return nil
}

// TestCardActionTimeoutSendsConversationNotice 验证卡片异步处理超时后用户能收到明确反馈。
func TestCardActionTimeoutSendsConversationNotice(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardActionTimeout = 20 * time.Millisecond
	sender := &cardTimeoutNoticeSender{texts: make(chan string, 1)}
	adapter.sender = sender
	blocked := make(chan struct{})
	event := cardChoiceEventForOrderTest("feishu:tenant:dm:oc_1:ou_user")
	event.Event.Action.Value["choice"] = "/cx ls"

	_, err := adapter.handleCardActionEvent(context.Background(), event, func(context.Context, platform.IncomingMessage, platform.Replier) {
		<-blocked
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-sender.texts:
		if !strings.Contains(got, "oc_1:") || !strings.Contains(got, "等待超时") {
			t.Fatalf("timeout notice=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("卡片处理超时后未发送用户反馈")
	}
	close(blocked)
}

func TestInlineCardActionFallsBackToSeparateResultAfterCallbackBudget(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardInlineTimeout = 20 * time.Millisecond
	sender := &cardTimeoutNoticeSender{texts: make(chan string, 1)}
	adapter.sender = sender
	release := make(chan struct{})
	event := cardChoiceEventForOrderTest("feishu:tenant:dm:oc_1:ou_user")
	event.Event.Action.Value["choice"] = "/cx ls"

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, _ platform.IncomingMessage, reply platform.Replier) {
		<-release
		_ = reply.SendText(ctx, "较慢的导航结果")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPendingChoiceCard(t, resp.Card, "会话 A", "正在处理")
	close(release)
	select {
	case got := <-sender.texts:
		if !strings.Contains(got, "较慢的导航结果") {
			t.Fatalf("fallback result=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("超出回调预算后未单独发送最终结果")
	}
}

func TestSlowSessionSwitchUpdatesOriginalCardAfterCallbackBudget(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardInlineTimeout = 20 * time.Millisecond
	sender := &deferredPatchSender{patches: make(chan string, 1), texts: make(chan string, 1)}
	adapter.sender = sender
	release := make(chan struct{})
	event := cardChoiceEventForOrderTest("feishu:tenant:dm:oc_1:ou_user")
	event.Event.Action.Value["choice"] = "/cx switch thread-a"

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, _ platform.IncomingMessage, reply platform.Replier) {
		<-release
		_ = reply.SendText(ctx, "已切换到会话 A")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPendingChoiceCard(t, resp.Card, "会话 A", "本卡片更新结果")
	close(release)
	select {
	case got := <-sender.patches:
		if !strings.Contains(got, "om_card:") || !strings.Contains(got, "已切换到会话 A") {
			t.Fatalf("patched card=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("慢切换完成后未更新原卡")
	}
	select {
	case got := <-sender.texts:
		t.Fatalf("更新原卡成功后不应单独发送结果: %q", got)
	default:
	}
}

func TestTimedOutSessionSwitchUpdatesOriginalCard(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardInlineTimeout = 5 * time.Millisecond
	adapter.cardActionTimeout = 20 * time.Millisecond
	sender := &deferredPatchSender{patches: make(chan string, 1), texts: make(chan string, 1)}
	adapter.sender = sender
	blocked := make(chan struct{})
	event := cardChoiceEventForOrderTest("feishu:tenant:dm:oc_1:ou_user")
	event.Event.Action.Value["choice"] = "/cx switch thread-a"

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(context.Context, platform.IncomingMessage, platform.Replier) {
		<-blocked
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPendingChoiceCard(t, resp.Card, "会话 A", "本卡片更新结果")
	select {
	case got := <-sender.patches:
		if !strings.Contains(got, "om_card:") || !strings.Contains(got, "等待超时") {
			t.Fatalf("timed out card=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("切换超时后未更新原卡")
	}
	close(blocked)
}
