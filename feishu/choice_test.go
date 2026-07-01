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

func TestBuildChoiceCardUsesCardKitV2ButtonValues(t *testing.T) {
	cardJSON, err := buildChoiceCard("请选择", []platform.Choice{{ID: "1", Label: "继续"}}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("card json invalid: %v", err)
	}
	if card["schema"] != "2.0" {
		t.Fatalf("schema=%#v, want CardKit 2.0", card["schema"])
	}
	if _, ok := card["elements"]; ok {
		t.Fatalf("top-level elements exists, want CardKit 2.0 body.elements")
	}
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	button := elements[1].(map[string]any)
	if button["tag"] != "button" {
		t.Fatalf("button tag=%#v, want direct CardKit 2.0 button element", button["tag"])
	}
	value := button["value"].(map[string]any)
	if value["action"] != cardActionChoice || value["choice"] != "1" || value["conv"] != "feishu:ou_user" {
		t.Fatalf("button value=%#v, want choice payload", value)
	}
}

func TestBuildChoiceCardMarksApprovalButtons(t *testing.T) {
	cardJSON, err := buildChoiceCard("Codex 请求执行敏感操作，请确认：\n\n{\"cmd\":\"date\",\"cwd\":\"/tmp/work\"}", []platform.Choice{{ID: "allow", Label: "允许本次"}}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	card := decodeCardJSON(t, cardJSON)
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	button := elements[1].(map[string]any)
	value := button["value"].(map[string]any)
	if value["kind"] != "approval" || value["label"] != "允许本次" || value["summary"] == "" {
		t.Fatalf("button value=%#v, want approval kind and label", value)
	}
}

func TestBuildChoiceCardUsesApprovalKeyMetadata(t *testing.T) {
	cardJSON, err := buildChoiceCard("Codex 请求执行敏感操作，请确认：\n\n{\"cmd\":\"date\"}", []platform.Choice{{
		ID:       "accept",
		Label:    "accept",
		Metadata: map[string]string{"approval_key": "approval-key-from-handler"},
	}}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	card := decodeCardJSON(t, cardJSON)
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	value := elements[1].(map[string]any)["value"].(map[string]any)
	if value["approval_key"] != "approval-key-from-handler" {
		t.Fatalf("button value=%#v, want metadata approval key", value)
	}
}

func TestBuildChoiceCardUsesTaskCardIDMetadata(t *testing.T) {
	cardJSON, err := buildChoiceCard("Codex 请求执行敏感操作，请确认：\n\n{\"cmd\":\"date\"}", []platform.Choice{{
		ID:       "accept",
		Label:    "accept",
		Metadata: map[string]string{"task_card_id": "card-task-1"},
	}}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	card := decodeCardJSON(t, cardJSON)
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	value := elements[1].(map[string]any)["value"].(map[string]any)
	if value["task_card_id"] != "card-task-1" {
		t.Fatalf("button value=%#v, want task card id", value)
	}
}

func TestBuildChoiceCardDoesNotMarkNormalChoicesAsApproval(t *testing.T) {
	cardJSON, err := buildChoiceCard("请选择工作空间", []platform.Choice{{ID: "/cx cd 0", Label: "weclaw"}}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	card := decodeCardJSON(t, cardJSON)
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	button := elements[1].(map[string]any)
	value := button["value"].(map[string]any)
	if value["kind"] != nil {
		t.Fatalf("button value=%#v, want no approval kind", value)
	}
}

func TestParseCardAction(t *testing.T) {
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_msg"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action": cardActionChoice,
				"choice": "2",
				"conv":   "feishu:ou_user",
			}},
		},
	}

	action, ok := parseCardAction(event)

	if !ok {
		t.Fatal("parseCardAction ok=false, want true")
	}
	if action.Action != cardActionChoice || action.Choice != "2" || action.Conv != "feishu:ou_user" {
		t.Fatalf("action=%#v, want normalized choice", action)
	}
	if action.UserID != "ou_user" || action.ChatID != "oc_chat" || action.MessageID != "om_msg" {
		t.Fatalf("action=%#v, want operator and context ids", action)
	}
}

func TestHandleCardActionEventReturnsApprovalStatusCard(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_msg"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action":  cardActionChoice,
				"choice":  "allow",
				"kind":    "approval",
				"label":   "允许本次",
				"summary": "command: date\ncwd: /tmp/work",
			}},
		},
	}
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want card update", resp)
	}
	card, ok := resp.Card.Data.(map[string]any)
	if !ok {
		t.Fatalf("response card data=%T, want card object", resp.Card.Data)
	}
	header := card["header"].(map[string]any)
	if header["template"] != "green" {
		t.Fatalf("header=%#v, want handled green card", header)
	}
	body := card["body"].(map[string]any)
	content := body["elements"].([]map[string]any)[0]["content"].(string)
	if !strings.Contains(content, "✅ 已授权") || !strings.Contains(content, "允许本次") || !strings.Contains(content, "command: date") {
		t.Fatalf("content=%q, want compact allow status", content)
	}
	if strings.Contains(content, "{") || strings.Contains(content, "}") || len(body["elements"].([]map[string]any)) != 1 {
		t.Fatalf("content=%q, want compact card without verbose JSON or buttons", content)
	}
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestBuildChoiceHandledCardShowsDenyStatus(t *testing.T) {
	card := buildChoiceHandledCard(parsedCardAction{Choice: "deny", Label: "拒绝", Summary: "command: rm file"})
	if card.Type != "raw" {
		t.Fatalf("card type=%q, want raw for callback card update", card.Type)
	}
	data := card.Data.(map[string]any)
	header := data["header"].(map[string]any)
	if header["template"] != "red" {
		t.Fatalf("header=%#v, want red denied card", header)
	}
	body := data["body"].(map[string]any)
	content := body["elements"].([]map[string]any)[0]["content"].(string)
	if !strings.Contains(content, "❌ 已拒绝") || !strings.Contains(content, "拒绝") {
		t.Fatalf("content=%q, want deny status", content)
	}
}

func TestBuildChoiceHandledCardShowsCancelAsDenyStatus(t *testing.T) {
	card := buildChoiceHandledCard(parsedCardAction{Choice: "cancel", Label: "cancel", Summary: "command: rm file"})
	data := card.Data.(map[string]any)
	header := data["header"].(map[string]any)
	if header["template"] != "red" {
		t.Fatalf("header=%#v, want red denied card", header)
	}
	body := data["body"].(map[string]any)
	content := body["elements"].([]map[string]any)[0]["content"].(string)
	if !strings.Contains(content, "❌ 已拒绝") || !strings.Contains(content, "cancel") {
		t.Fatalf("content=%q, want cancel denied status", content)
	}
}

func TestBuildChoiceHandledCardCallbackJSONUsesRawType(t *testing.T) {
	resp := &callback.CardActionTriggerResponse{
		Card: buildChoiceHandledCard(parsedCardAction{Choice: "allow", Label: "允许本次", Summary: "command: date"}),
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal callback response: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("callback response json invalid: %v", err)
	}
	card := payload["card"].(map[string]any)
	if card["type"] != "raw" {
		t.Fatalf("callback card type=%#v, want raw", card["type"])
	}
	if card["type"] == "card_json" {
		t.Fatalf("callback card must not use CardKit API type card_json: %s", string(data))
	}
}

func TestApprovalSummaryTruncatesLongCommandAndCwd(t *testing.T) {
	longValue := strings.Repeat("很长路径", 80)
	cardJSON, err := buildChoiceCard("Codex 请求执行敏感操作，请确认：\n\n"+`{"cmd":"`+longValue+`","cwd":"/tmp/`+longValue+`"}`, []platform.Choice{{ID: "allow", Label: "允许本次"}}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	card := decodeCardJSON(t, cardJSON)
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	value := elements[1].(map[string]any)["value"].(map[string]any)
	summary := value["summary"].(string)
	if !strings.Contains(summary, "...") {
		t.Fatalf("summary=%q, want truncated summary", summary)
	}
	if len([]rune(summary)) > 180 {
		t.Fatalf("summary length=%d, want compact summary", len([]rune(summary)))
	}
}
