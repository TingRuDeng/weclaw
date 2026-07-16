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
