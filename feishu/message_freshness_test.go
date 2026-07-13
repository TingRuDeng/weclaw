package feishu

import (
	"bytes"
	"context"
	"log"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// TestHandleMessageEventRejectsMessageCreatedBeforeRun 验证服务停机期间的积压消息不会进入业务层。
func TestHandleMessageEventRejectsMessageCreatedBeforeRun(t *testing.T) {
	startedAt := time.Date(2026, 7, 13, 20, 47, 52, 0, time.Local)
	adapter, _ := startFreshnessTestAdapter(t, startedAt)
	event := newFreshnessDMEvent("om_before_run", startedAt.Add(-time.Second), "/restart")

	if got := dispatchFeishuEvents(adapter, event); got != 0 {
		t.Fatalf("dispatches=%d，期望启动前消息不分发", got)
	}
}

// TestHandleMessageEventRejectsMessageOlderThanMaximumAge 验证运行期间首次到达的超龄消息也会被拦截。
func TestHandleMessageEventRejectsMessageOlderThanMaximumAge(t *testing.T) {
	startedAt := time.Date(2026, 7, 13, 20, 47, 52, 0, time.Local)
	adapter, setNow := startFreshnessTestAdapter(t, startedAt)
	setNow(startedAt.Add(3 * time.Minute))
	event := newFreshnessDMEvent("om_too_old", startedAt.Add(time.Second), "/cc ls")

	if got := dispatchFeishuEvents(adapter, event); got != 0 {
		t.Fatalf("dispatches=%d，期望超过最大年龄的消息不分发", got)
	}
}

// TestHandleMessageEventRejectsInvalidCreateTime 验证非法外部时间戳不能绕过重放保护。
func TestHandleMessageEventRejectsInvalidCreateTime(t *testing.T) {
	startedAt := time.Date(2026, 7, 13, 20, 47, 52, 0, time.Local)
	adapter, _ := startFreshnessTestAdapter(t, startedAt)
	event := newDMEvent(dedupTestEventOptions{
		MessageID: "om_invalid_time", EventID: "evt_invalid_time",
		CreateTime: "invalid", Text: "/cx ls",
	})

	if got := dispatchFeishuEvents(adapter, event); got != 0 {
		t.Fatalf("dispatches=%d，期望非法时间戳消息不分发", got)
	}
}

// TestHandleMessageEventDispatchesFreshMessage 验证重放保护不影响正常实时消息。
func TestHandleMessageEventDispatchesFreshMessage(t *testing.T) {
	startedAt := time.Date(2026, 7, 13, 20, 47, 52, 0, time.Local)
	adapter, setNow := startFreshnessTestAdapter(t, startedAt)
	setNow(startedAt.Add(30 * time.Second))
	event := newFreshnessDMEvent("om_fresh", startedAt.Add(time.Second), "正常消息")

	if got := dispatchFeishuEvents(adapter, event); got != 1 {
		t.Fatalf("dispatches=%d，期望实时消息正常分发", got)
	}
}

// TestHandleMessageEventAllowsHistoricalMessageWhenProtectionDisabled 验证显式配置 0 可以关闭整套时效保护。
func TestHandleMessageEventAllowsHistoricalMessageWhenProtectionDisabled(t *testing.T) {
	startedAt := time.Date(2026, 7, 13, 20, 47, 52, 0, time.Local)
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	setter, ok := any(adapter).(interface{ SetMaxMessageAge(time.Duration) })
	if !ok {
		t.Fatal("Adapter 缺少 SetMaxMessageAge 配置入口")
	}
	setter.SetMaxMessageAge(0)
	adapter, _ = runFreshnessTestAdapter(t, adapter, startedAt)
	event := newFreshnessDMEvent("om_disabled", startedAt.Add(-time.Hour), "/cx ls")

	if got := dispatchFeishuEvents(adapter, event); got != 1 {
		t.Fatalf("dispatches=%d，期望关闭保护后继续分发", got)
	}
}

// TestStaleMessageLogContainsTimingWithoutContent 验证诊断日志包含时间边界且不泄露消息正文。
func TestStaleMessageLogContainsTimingWithoutContent(t *testing.T) {
	startedAt := time.Date(2026, 7, 13, 20, 47, 52, 0, time.Local)
	adapter, _ := startFreshnessTestAdapter(t, startedAt)
	event := newFreshnessDMEvent("om_log", startedAt.Add(-time.Hour), "敏感正文")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	dispatchFeishuEvents(adapter, event)

	output := logs.String()
	for _, want := range []string{"account=cli_a", "message=om_log", "created_at=", "received_at=", "age=", "reason=created_before_run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("日志=%q，缺少 %q", output, want)
		}
	}
	if strings.Contains(output, "敏感正文") {
		t.Fatalf("日志不应包含消息正文：%q", output)
	}
}

// startFreshnessTestAdapter 创建使用固定时钟的运行中飞书适配器。
func startFreshnessTestAdapter(t *testing.T, startedAt time.Time) (*Adapter, func(time.Time)) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	return runFreshnessTestAdapter(t, adapter, startedAt)
}

// runFreshnessTestAdapter 启动指定适配器，并返回安全推进测试时钟的函数。
func runFreshnessTestAdapter(t *testing.T, adapter *Adapter, startedAt time.Time) (*Adapter, func(time.Time)) {
	t.Helper()
	current := startedAt
	adapter.now = func() time.Time { return current }
	adapter.validate = func(context.Context, Credentials) error { return nil }
	ws := &fakeWSRunner{started: make(chan struct{}), closed: make(chan struct{})}
	adapter.wsFactory = func(*dispatcher.EventDispatcher) wsRunner { return ws }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- adapter.Run(ctx, func(context.Context, platform.IncomingMessage, platform.Replier) {})
	}()
	<-ws.started
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Adapter.Run 退出失败：%v", err)
		}
	})
	return adapter, func(next time.Time) { current = next }
}

// newFreshnessDMEvent 构造带毫秒创建时间的飞书私聊事件。
func newFreshnessDMEvent(messageID string, createdAt time.Time, text string) *larkim.P2MessageReceiveV1 {
	return newDMEvent(dedupTestEventOptions{
		MessageID:  messageID,
		EventID:    "evt_" + messageID,
		CreateTime: strconv.FormatInt(createdAt.UnixMilli(), 10),
		Text:       text,
	})
}
