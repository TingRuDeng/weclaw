package feishu

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestReasoningChoiceCollapsesCardAndReplaysCommand(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_reasoning"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action": cardActionChoice,
				"choice": "/reasoning high",
				"label":  "high",
				"conv":   "feishu:ou_user",
			}},
		},
	}
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		dispatched <- msg
	})
	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	assertInlineCardContent(t, resp.Card, "已完成：high")
	select {
	case msg := <-dispatched:
		if msg.RawCommand == nil || msg.RawCommand.Value["choice"] != "/reasoning high" {
			t.Fatalf("RawCommand=%#v，期望回放完整推理强度命令", msg.RawCommand)
		}
	case <-time.After(time.Second):
		t.Fatal("等待推理强度卡片回调超时")
	}
}

func TestSubmittedChoiceCardUsesUserFacingPendingStatus(t *testing.T) {
	tests := []struct {
		name   string
		choice string
		want   string
	}{
		{name: "workspace loading", choice: "/cx cd 9", want: "正在加载中，请稍后……"},
		{name: "codex session switch", choice: "/cx switch thread-1", want: "正在切换中，请稍后……"},
		{name: "claude session switch", choice: "/cc switch session-1", want: "正在切换中，请稍后……"},
		{name: "codex account switch", choice: "/cx account confirm token-1", want: "正在切换中，请稍后……"},
		{name: "other operation", choice: "/stop", want: "正在处理中，请稍后……"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := buildSubmittedChoiceCard(parsedCardAction{Choice: tt.choice, Label: "目标"})
			assertPendingChoiceCard(t, card, "目标", tt.want)
		})
	}
}

func TestHandleCardActionEventDoesNotCollapseUnrecognizedCard(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Action:   &callback.CallBackAction{Value: map[string]interface{}{}},
		},
	}

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(context.Context, platform.IncomingMessage, platform.Replier) {
		t.Fatal("unrecognized card action must not be dispatched")
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type == "success" || resp.Card != nil {
		t.Fatalf("response=%#v, want warning without card update", resp)
	}
}
