package feishu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildTaskCardUsesCollapsibleProgressPanel(t *testing.T) {
	statuses := []struct {
		status   string
		expanded bool
	}{{cardStatusThinking, true}, {cardStatusStreaming, true}, {cardStatusSuperseded, false}, {cardStatusDone, false}, {cardStatusStopped, false}, {cardStatusError, false}}
	for _, tt := range statuses {
		raw, err := buildCardV2(cardOptions{Status: tt.status, Title: "Codex", Summary: "摘要", Content: "详情", Collapsible: true, Expanded: tt.expanded})
		if err != nil {
			t.Fatal(err)
		}
		card := decodeCardJSON(t, raw)
		elements := card["body"].(map[string]any)["elements"].([]any)
		summaryIndex, panelIndex := -1, -1
		for i, element := range elements {
			id := element.(map[string]any)["element_id"]
			if id == cardProgressSummaryID {
				summaryIndex = i
			}
			if id == cardProgressPanelID {
				panelIndex = i
			}
		}
		if summaryIndex < 0 || panelIndex < 0 || summaryIndex >= panelIndex {
			t.Fatalf("status=%s elements=%#v", tt.status, elements)
		}
		panel := elements[panelIndex].(map[string]any)
		if panel["tag"] != "collapsible_panel" || panel["element_id"] != cardProgressPanelID || panel["expanded"] != tt.expanded {
			t.Fatalf("status=%s panel=%#v", tt.status, panel)
		}
		header := panel["header"].(map[string]any)["title"].(map[string]any)
		wantHeader := "完整进度"
		if !tt.expanded {
			wantHeader = "展开完整进度"
		}
		if header["content"] != wantHeader {
			t.Fatalf("header=%#v", header)
		}
		inside := panel["elements"].([]any)
		if len(inside) != 1 || inside[0].(map[string]any)["element_id"] != cardMainContentID {
			t.Fatalf("inside=%#v", inside)
		}
	}
	raw, err := buildCardV2(cardOptions{Status: cardStatusDone, Content: "结果"})
	if err != nil {
		t.Fatal(err)
	}
	elements := decodeCardJSON(t, raw)["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 2 {
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
	if len(elements) != 3 || elements[0].(map[string]any)["element_id"] != cardProgressSummaryID ||
		elements[1].(map[string]any)["element_id"] != "approval_records" ||
		elements[2].(map[string]any)["element_id"] != cardProgressPanelID {
		t.Fatalf("elements=%#v, want summary and approvals before progress panel", elements)
	}
	panelElements := elements[2].(map[string]any)["elements"].([]any)
	if len(panelElements) != 2 {
		t.Fatalf("panel elements=%#v, want progress and bottom collapse control", panelElements)
	}
	if panelElements[0].(map[string]any)["element_id"] != cardMainContentID {
		t.Fatalf("panel elements=%#v, want progress first", panelElements)
	}
	button := panelElements[1].(map[string]any)
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

func TestCollapsedTaskCardKeepsNativeExpandHeaderAtVisibleBottom(t *testing.T) {
	registry := newTaskCardRegistry()
	registry.record("card-task-1", cardOptions{
		Status: cardStatusDone, Title: "Codex", Summary: "摘要", Content: "完整时间线",
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
	if len(elements) != 4 || elements[3].(map[string]any)["element_id"] != cardProgressPanelID {
		t.Fatalf("elements=%#v, want collapsed progress panel as final visible body element", elements)
	}
	panel := elements[3].(map[string]any)
	if panel["expanded"] != false {
		t.Fatalf("panel expanded=%v, want false", panel["expanded"])
	}
	header := panel["header"].(map[string]any)["title"].(map[string]any)
	if header["content"] != "展开完整进度" {
		t.Fatalf("header=%#v", header)
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
		{cardStatusDone, "green", "**已完成**"},
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
		body := card["body"].(map[string]any)
		statusElement := body["elements"].([]any)[0].(map[string]any)
		if statusElement["content"] != tt.label {
			t.Fatalf("status=%s label=%v, want %s", tt.status, statusElement["content"], tt.label)
		}
	}
}

func TestBuildCardV2DoneWithoutContentRendersStatusOnly(t *testing.T) {
	raw, err := buildCardV2(cardOptions{Status: cardStatusDone})
	if err != nil {
		t.Fatalf("buildCardV2 error: %v", err)
	}
	card := decodeCardJSON(t, raw)
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if len(elements) != 1 || elements[0].(map[string]any)["content"] != "**已完成**" {
		t.Fatalf("done card body=%#v, want compact terminal status only", card["body"])
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
	if len(elements) != 3 {
		t.Fatalf("elements=%d, want approval record element", len(elements))
	}
	main := elements[1].(map[string]any)
	if main["element_id"] != cardMainContentID || main["content"] != "最终回答" {
		t.Fatalf("main element=%#v, want final content without status row", main)
	}
	approval := elements[2].(map[string]any)
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
