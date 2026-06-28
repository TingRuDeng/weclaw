package feishu

import (
	"encoding/json"
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
		{cardStatusThinking, "blue", "**正在思考**"},
		{cardStatusStreaming, "blue", "**正在回复**"},
		{cardStatusDone, "green", "**已完成**"},
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
		body := card["body"].(map[string]any)
		statusElement := body["elements"].([]any)[0].(map[string]any)
		if statusElement["content"] != tt.label {
			t.Fatalf("status=%s label=%v, want %s", tt.status, statusElement["content"], tt.label)
		}
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

// decodeCardJSON 解码卡片 JSON，便于测试断言结构。
func decodeCardJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var card map[string]any
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("invalid card json: %v", err)
	}
	return card
}
