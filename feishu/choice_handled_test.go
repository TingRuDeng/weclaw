package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

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

func TestBuildChoiceHandledCardShowsExpiredStatus(t *testing.T) {
	card := buildChoiceHandledCard(parsedCardAction{Choice: "allow", Label: "允许本次", Summary: "command: date", Status: approvalStatusExpired})
	data := card.Data.(map[string]any)
	header := data["header"].(map[string]any)
	if header["template"] != "yellow" {
		t.Fatalf("header=%#v, want yellow expired card", header)
	}
	body := data["body"].(map[string]any)
	content := body["elements"].([]map[string]any)[0]["content"].(string)
	if !strings.Contains(content, "⚠️ 已过期") || !strings.Contains(content, "允许本次") {
		t.Fatalf("content=%q, want expired status", content)
	}
}

func TestBuildChoiceHandledCardShowsArchivedStatus(t *testing.T) {
	card := buildChoiceHandledCard(parsedCardAction{Choice: "allow", Label: "允许本次", Summary: "command: date", Status: approvalStatusArchived})
	data := card.Data.(map[string]any)
	header := data["header"].(map[string]any)
	if header["template"] != "green" {
		t.Fatalf("header=%#v, want green archived card", header)
	}
	body := data["body"].(map[string]any)
	content := body["elements"].([]map[string]any)[0]["content"].(string)
	if !strings.Contains(content, "✅ 已收纳到任务卡片") {
		t.Fatalf("content=%q, want archived status", content)
	}
	if strings.Contains(content, "command: date") || strings.Contains(content, "允许本次") {
		t.Fatalf("content=%q, want one-line archived status", content)
	}
}

func TestBuildChoiceHandledCardCompactsHandledApprovalSummary(t *testing.T) {
	card := buildChoiceHandledCard(parsedCardAction{Choice: "allow", Label: "允许本次", Summary: "command: apply_patch very long payload"})
	data := card.Data.(map[string]any)
	body := data["body"].(map[string]any)
	content := body["elements"].([]map[string]any)[0]["content"].(string)
	if !strings.Contains(content, "✅ 已授权") || !strings.Contains(content, "允许本次") {
		t.Fatalf("content=%q, want compact handled approval status", content)
	}
	if strings.Contains(content, "apply_patch") || strings.Contains(content, "payload") {
		t.Fatalf("content=%q, want no verbose approval summary", content)
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
