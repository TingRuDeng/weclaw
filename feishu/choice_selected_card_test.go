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
	assertSelectedChoiceCard(t, resp.Card, "high")
	select {
	case msg := <-dispatched:
		if msg.RawCommand == nil || msg.RawCommand.Value["choice"] != "/reasoning high" {
			t.Fatalf("RawCommand=%#v，期望回放完整推理强度命令", msg.RawCommand)
		}
	case <-time.After(time.Second):
		t.Fatal("等待推理强度卡片回调超时")
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

// assertSelectedChoiceCard 验证普通交互卡片已收纳，且不再保留可重复点击的按钮。
func assertSelectedChoiceCard(t *testing.T, card *callback.Card, label string) {
	t.Helper()
	if card == nil || card.Type != "raw" {
		t.Fatalf("card=%#v, want raw selected card", card)
	}
	cardData, ok := card.Data.(map[string]any)
	if !ok {
		t.Fatalf("card data=%T, want card object", card.Data)
	}
	header, ok := cardData["header"].(map[string]any)
	if !ok || header["template"] != "blue" {
		t.Fatalf("header=%#v, want neutral blue selected card", header)
	}
	data, err := json.Marshal(card.Data)
	if err != nil {
		t.Fatalf("marshal selected card: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "已选择："+label) {
		t.Fatalf("card=%s, want selected label %q", content, label)
	}
	if strings.Contains(content, `"tag":"button"`) {
		t.Fatalf("card=%s, want no button after selection", content)
	}
}
