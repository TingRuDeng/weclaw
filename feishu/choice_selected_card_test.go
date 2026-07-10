package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

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
