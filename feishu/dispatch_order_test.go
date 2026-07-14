package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// TestCardActionBlocksLaterMessageDispatch 验证卡片切换完成前，同窗口普通消息不能抢跑。
func TestCardActionBlocksLaterMessageDispatch(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	messageEvent := newMessageEvent("p2p", "text", `{"text":"当前项目目录是什么"}`)
	scope := ExtractFeishuSessionScope(messageEvent)
	scope.AccountID = adapter.creds.AppID
	sessionKey := BuildFeishuSessionKey(scope)
	cardEvent := cardChoiceEventForOrderTest(sessionKey)
	cardStarted := make(chan struct{})
	releaseCard := make(chan struct{})
	messageDispatched := make(chan struct{}, 1)
	dispatch := func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		if msg.RawCommand != nil {
			close(cardStarted)
			<-releaseCard
			return
		}
		messageDispatched <- struct{}{}
	}

	resp, err := adapter.handleCardActionEvent(context.Background(), cardEvent, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	assertSubmittedChoiceCard(t, resp.Card, "会话 A")
	<-cardStarted
	messageDone := make(chan error, 1)
	go func() { messageDone <- adapter.handleMessageEvent(context.Background(), messageEvent, dispatch) }()
	select {
	case <-messageDispatched:
		t.Fatal("普通消息越过了尚未完成的卡片会话切换")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseCard)
	select {
	case <-messageDispatched:
	case <-time.After(time.Second):
		t.Fatal("卡片切换完成后普通消息仍未继续分发")
	}
	if err := <-messageDone; err != nil {
		t.Fatal(err)
	}
}

func TestQueuedMessageUsesDetachedEventContext(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := newMessageEvent("p2p", "text", `{"text":"继续"}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dispatched := false
	err := adapter.handleMessageEvent(ctx, event, func(ctx context.Context, _ platform.IncomingMessage, _ platform.Replier) {
		dispatched = ctx.Err() == nil
	})
	if err != nil || !dispatched {
		t.Fatalf("err=%v dispatched=%v，消息不应继承已取消的事件 context", err, dispatched)
	}
}

func TestDispatchWaitTimeoutPreservesPreviousTicket(t *testing.T) {
	sequencer := newFeishuDispatchSequencer()
	first := sequencer.reserve("session")
	second := sequencer.reserve("session")
	blocked := make(chan struct{})
	go first.run(context.Background(), func() { <-blocked })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if second.run(ctx, func() {}) {
		t.Fatal("等待超时的 ticket 不应报告执行成功")
	}
	third := sequencer.reserve("session")
	thirdDone := make(chan struct{})
	go third.run(context.Background(), func() { close(thirdDone) })
	select {
	case <-thirdDone:
		close(blocked)
		t.Fatal("等待超时后后继票据越过了原前序操作")
	case <-time.After(2 * testDispatchWaitTimeout):
	}
	close(blocked)
	select {
	case <-thirdDone:
	case <-time.After(time.Second):
		t.Fatal("原前序操作结束后后继票据仍未执行")
	}
}

// TestDispatchWaitTimeoutRunsCurrentTicket 验证前序任务长期阻塞时当前消息仍会进入业务层。
func TestDispatchWaitTimeoutRunsCurrentTicket(t *testing.T) {
	sequencer := newFeishuDispatchSequencer()
	first := sequencer.reserve("session")
	second := sequencer.reserve("session")
	blocked := make(chan struct{})
	go first.run(context.Background(), func() { <-blocked })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	dispatched := false

	if second.runAfterWaitTimeout(ctx, func() { dispatched = true }) {
		t.Fatal("前序票据超时后不应报告有序执行")
	}
	if !dispatched {
		t.Fatal("前序票据超时后当前消息未进入业务层")
	}
	third := sequencer.reserve("session")
	thirdDone := make(chan struct{})
	go third.run(context.Background(), func() { close(thirdDone) })
	select {
	case <-thirdDone:
		close(blocked)
		t.Fatal("旁路控制命令切断了原前序队列")
	case <-time.After(2 * testDispatchWaitTimeout):
	}
	close(blocked)
	select {
	case <-thirdDone:
	case <-time.After(time.Second):
		t.Fatal("原前序操作结束后队列未恢复")
	}
}

// TestDispatchTimeoutKeepsQueueBlockedUntilDispatchReturns 验证超时反馈不会放行仍在执行操作的后继票据。
func TestDispatchTimeoutKeepsQueueBlockedUntilDispatchReturns(t *testing.T) {
	sequencer := newFeishuDispatchSequencer()
	first := sequencer.reserve("same-chat")
	ctx, cancel := context.WithTimeout(context.Background(), testDispatchWaitTimeout)
	defer cancel()
	blocked := make(chan struct{})
	firstResult := make(chan bool, 1)
	go func() {
		firstResult <- first.run(ctx, func() { <-blocked })
	}()
	if result := <-firstResult; result {
		t.Fatal("前序操作超过期限后仍报告成功")
	}

	second := sequencer.reserve("same-chat")
	secondDone := make(chan struct{})
	go second.run(context.Background(), func() { close(secondDone) })
	select {
	case <-secondDone:
		close(blocked)
		t.Fatal("超时操作尚未退出时后继票据已执行")
	case <-time.After(2 * testDispatchWaitTimeout):
	}
	close(blocked)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("超时操作退出后后继票据仍未执行")
	}
}

func cardChoiceEventForOrderTest(sessionKey string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Context:  &callback.Context{OpenChatID: "oc_1", OpenMessageID: "om_card"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			"action": cardActionChoice, "choice": "/cx switch thread-a",
			"label": "会话 A", feishuSessionMetadataKey: sessionKey,
		}},
	}}
}

func assertSubmittedChoiceCard(t *testing.T, card *callback.Card, label string) {
	t.Helper()
	data, err := json.Marshal(card.Data)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "已提交："+label) || !strings.Contains(content, "处理结果将单独发送") {
		t.Fatalf("card=%s，期望展示已提交状态", content)
	}
	if strings.Contains(content, "已选择：") || strings.Contains(content, `"tag":"button"`) {
		t.Fatalf("card=%s，不应提前声明已选择或保留按钮", content)
	}
}
