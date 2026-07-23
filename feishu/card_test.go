package feishu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCardV2IncludesStableMainContentElement(t *testing.T) {
	raw, err := buildCardV2(cardOptions{Status: cardStatusThinking, Title: "Codex", Content: "处理中"})
	if err != nil {
		t.Fatalf("buildCardV2 error: %v", err)
	}
	card := decodeCardJSON(t, raw)
	if card["schema"] != "2.0" {
		t.Fatalf("schema=%v, want 2.0", card["schema"])
	}
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	main := elements[1].(map[string]any)
	if main["element_id"] != cardMainContentID || main["content"] != "处理中" {
		t.Fatalf("main element=%#v, want stable main content", main)
	}
}

func TestBuildCardV2StatusTemplates(t *testing.T) {
	cases := []struct {
		status   string
		template string
		label    string
	}{
		{cardStatusThinking, "blue", "**思考中**"},
		{cardStatusStreaming, "blue", "**生成中**"},
		{cardStatusDone, "green", ""},
		{cardStatusError, "red", "**执行失败**"},
	}
	for _, tt := range cases {
		raw, err := buildCardV2(cardOptions{Status: tt.status})
		if err != nil {
			t.Fatalf("buildCardV2(%s) error: %v", tt.status, err)
		}
		card := decodeCardJSON(t, raw)
		header := card["header"].(map[string]any)
		if header["template"] != tt.template {
			t.Fatalf("status=%s template=%v, want %s", tt.status, header["template"], tt.template)
		}
		if tt.label == "" {
			if _, ok := card["body"]; ok {
				t.Fatalf("status=%s should not render a redundant completion body: %#v", tt.status, card["body"])
			}
			if strings.Contains(raw, "已完成") {
				t.Fatalf("status=%s card should not contain completion text: %s", tt.status, raw)
			}
			continue
		}
		body := card["body"].(map[string]any)
		statusElement := body["elements"].([]any)[0].(map[string]any)
		if statusElement["content"] != tt.label {
			t.Fatalf("status=%s label=%v, want %s", tt.status, statusElement["content"], tt.label)
		}
	}
}

func TestBuildCardV2DoneWithoutContentUsesGreenHeaderOnly(t *testing.T) {
	raw, err := buildCardV2(cardOptions{Status: cardStatusDone})
	if err != nil {
		t.Fatalf("buildCardV2 error: %v", err)
	}
	card := decodeCardJSON(t, raw)
	if _, ok := card["body"]; ok {
		t.Fatalf("done card body=%#v, want green header only", card["body"])
	}
	header := card["header"].(map[string]any)
	if header["template"] != "green" {
		t.Fatalf("template=%v, want green", header["template"])
	}
	if strings.Contains(raw, "已完成") {
		t.Fatalf("done card should not contain completion text: %s", raw)
	}
}

func TestBuildCardV2NormalizesUnknownStatus(t *testing.T) {
	raw, err := buildCardV2(cardOptions{Status: "unknown"})
	if err != nil {
		t.Fatalf("buildCardV2 error: %v", err)
	}
	card := decodeCardJSON(t, raw)
	header := card["header"].(map[string]any)
	if header["template"] != "blue" {
		t.Fatalf("template=%v, want blue", header["template"])
	}
}

func TestBuildCardV2AppendsApprovalRecords(t *testing.T) {
	raw, err := buildCardV2(cardOptions{
		Status:    cardStatusDone,
		Title:     "Codex",
		Content:   "最终回答",
		Approvals: []string{"✅ 已授权：accept\ncommand: date"},
	})
	if err != nil {
		t.Fatalf("buildCardV2 error: %v", err)
	}
	card := decodeCardJSON(t, raw)
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("elements=%d, want approval record element", len(elements))
	}
	main := elements[0].(map[string]any)
	if main["element_id"] != cardMainContentID || main["content"] != "最终回答" {
		t.Fatalf("main element=%#v, want final content without status row", main)
	}
	approval := elements[1].(map[string]any)
	content := approval["content"].(string)
	if !strings.Contains(content, "审批记录") || !strings.Contains(content, "command: date") {
		t.Fatalf("approval content=%q, want approval summary", content)
	}
}

// decodeCardJSON 解码卡片 JSON，便于测试断言结构。
func decodeCardJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var card map[string]any
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("invalid card json: %v", err)
	}
	return card
}
