package feishu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildTaskCardUsesExplicitProgressVisibilityControls(t *testing.T) {
	statuses := []struct {
		status   string
		expanded bool
	}{{cardStatusThinking, true}, {cardStatusStreaming, true}, {cardStatusSuperseded, false}, {cardStatusDone, false}, {cardStatusStopped, false}, {cardStatusError, false}}
	for _, tt := range statuses {
		raw, err := buildCardV2(cardOptions{
			Status: tt.status, Title: "Codex", Summary: "摘要", Content: "详情",
			Collapsible: true, Expanded: tt.expanded, taskCardID: "card-task-1",
		})
		if err != nil {
			t.Fatal(err)
		}
		card := decodeCardJSON(t, raw)
		elements := card["body"].(map[string]any)["elements"].([]any)
		mainVisible, expandVisible, collapseVisible, summaryVisible := false, false, false, false
		for _, rawElement := range elements {
			element := rawElement.(map[string]any)
			switch element["element_id"] {
			case cardMainContentID:
				mainVisible = true
			case cardProgressExpandID:
				expandVisible = true
			case cardProgressCollapseID:
				collapseVisible = true
			case "progress_summary":
				summaryVisible = true
			}
		}
		if !mainVisible || expandVisible == tt.expanded || collapseVisible != tt.expanded || summaryVisible {
			t.Fatalf("status=%s expanded=%t elements=%#v", tt.status, tt.expanded, elements)
		}
	}
	raw, err := buildCardV2(cardOptions{Status: cardStatusDone, Content: "结果"})
	if err != nil {
		t.Fatal(err)
	}
	elements := decodeCardJSON(t, raw)["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 1 {
		t.Fatalf("normal elements=%#v", elements)
	}
	for _, element := range elements {
		if element.(map[string]any)["tag"] == "collapsible_panel" {
			t.Fatal("normal result unexpectedly collapsible")
		}
	}
}

func TestExpandedTaskCardPlacesCollapseControlAfterProgressAndApprovals(t *testing.T) {
	registry := newTaskCardRegistry()
	registry.record("card-task-1", cardOptions{
		Status: cardStatusStreaming, Title: "Codex", Summary: "摘要", Content: "完整时间线",
		Approvals: []string{"允许本次：读取文件"}, Collapsible: true, Expanded: true,
		InlineActiveStatus: true,
	})
	opts, ok := registry.snapshot("card-task-1")
	if !ok {
		t.Fatal("task card snapshot missing")
	}
	raw, err := buildCardV2(opts)
	if err != nil {
		t.Fatal(err)
	}
	elements := decodeCardJSON(t, raw)["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 3 || elements[0].(map[string]any)["element_id"] != "approval_records" ||
		elements[1].(map[string]any)["element_id"] != cardMainContentID ||
		elements[2].(map[string]any)["element_id"] != cardProgressCollapseID {
		t.Fatalf("elements=%#v, want approvals, complete progress and bottom collapse control", elements)
	}
	button := elements[2].(map[string]any)
	if button["tag"] != "button" || button["element_id"] != "progress_collapse" {
		t.Fatalf("bottom element=%#v, want collapse button", button)
	}
	if button["text"].(map[string]any)["content"] != "收起完整进度" {
		t.Fatalf("button text=%#v", button["text"])
	}
	behaviors := button["behaviors"].([]any)
	value := behaviors[0].(map[string]any)["value"].(map[string]any)
	if value["action"] != "task_progress_collapse" || value["task_card_id"] != "card-task-1" {
		t.Fatalf("button value=%#v, want task-card collapse action", value)
	}
}

func TestTaskCardProgressControlsAreMutuallyExclusiveButtons(t *testing.T) {
	tests := []struct {
		name          string
		expanded      bool
		wantControlID string
		wantText      string
		wantAction    string
	}{
		{name: "collapsed", wantControlID: "progress_expand", wantText: "展开完整进度", wantAction: "task_progress_expand"},
		{name: "expanded", expanded: true, wantControlID: cardProgressCollapseID, wantText: "收起完整进度", wantAction: "task_progress_collapse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := buildCardV2(cardOptions{
				Status: cardStatusStreaming, Title: "Codex", Summary: "摘要", Preview: "最近五条", Content: "完整时间线",
				Collapsible: true, Expanded: tt.expanded, taskCardID: "card-task-1",
			})
			if err != nil {
				t.Fatal(err)
			}
			elements := decodeCardJSON(t, raw)["body"].(map[string]any)["elements"].([]any)
			foundMain, foundSummary := false, false
			controls := make([]map[string]any, 0, 1)
			for _, rawElement := range elements {
				element := rawElement.(map[string]any)
				if element["element_id"] == cardMainContentID {
					foundMain = true
				}
				if element["element_id"] == "progress_summary" {
					foundSummary = true
				}
				if element["tag"] == "button" {
					controls = append(controls, element)
				}
			}
			if !foundMain {
				t.Fatalf("main visible=%v, want one progress body; elements=%#v", foundMain, elements)
			}
			if foundSummary {
				t.Fatalf("elements=%#v, want one progress body without duplicate summary", elements)
			}
			if len(controls) != 1 {
				t.Fatalf("controls=%#v, want exactly one progress control", controls)
			}
			control := controls[0]
			if control["element_id"] != tt.wantControlID || control["type"] != "default" ||
				control["text"].(map[string]any)["content"] != tt.wantText {
				t.Fatalf("control=%#v", control)
			}
			value := control["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
			if value["action"] != tt.wantAction || value["task_card_id"] != "card-task-1" {
				t.Fatalf("control value=%#v", value)
			}
		})
	}
}

func TestCollapsedTaskCardKeepsExplicitExpandButtonAtVisibleBottom(t *testing.T) {
	registry := newTaskCardRegistry()
	registry.record("card-task-1", cardOptions{
		Status: cardStatusDone, Title: "Codex", Summary: "摘要", Preview: "最近五条", Content: "完整时间线",
		Approvals: []string{"允许本次：读取文件"}, Collapsible: true, Expanded: false,
	})
	opts, ok := registry.snapshot("card-task-1")
	if !ok {
		t.Fatal("task card snapshot missing")
	}
	raw, err := buildCardV2(opts)
	if err != nil {
		t.Fatal(err)
	}
	elements := decodeCardJSON(t, raw)["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 3 || elements[1].(map[string]any)["element_id"] != cardMainContentID ||
		elements[1].(map[string]any)["content"] != "最近五条" ||
		elements[2].(map[string]any)["element_id"] != cardProgressExpandID {
		t.Fatalf("elements=%#v, want preview and expand control as final visible body elements", elements)
	}
	button := elements[2].(map[string]any)
	if button["text"].(map[string]any)["content"] != "展开完整进度" || button["type"] != "default" {
		t.Fatalf("button=%#v", button)
	}
}

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
		{"stopped", "grey", "**已停止**"},
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
		body, bodyExists := card["body"].(map[string]any)
		if tt.label == "" {
			if bodyExists {
				t.Fatalf("status=%s body=%#v, want header-only status", tt.status, body)
			}
			continue
		}
		statusElement := body["elements"].([]any)[0].(map[string]any)
		if statusElement["content"] != tt.label {
			t.Fatalf("status=%s label=%v, want %s", tt.status, statusElement["content"], tt.label)
		}
	}
}

func TestBuildCardV2DoneWithoutContentUsesGreenHeaderWithoutRedundantBody(t *testing.T) {
	raw, err := buildCardV2(cardOptions{Status: cardStatusDone})
	if err != nil {
		t.Fatalf("buildCardV2 error: %v", err)
	}
	card := decodeCardJSON(t, raw)
	if body, ok := card["body"]; ok {
		t.Fatalf("done card body=%#v, want green header without redundant completion body", body)
	}
	header := card["header"].(map[string]any)
	if header["template"] != "green" {
		t.Fatalf("template=%v, want green", header["template"])
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
