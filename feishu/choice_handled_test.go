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

func TestChoiceCommandResultTemplateUsesTerminalStateColors(t *testing.T) {
	tests := []struct {
		name    string
		command string
		content string
		want    string
	}{
		{name: "ordinary information", command: "/cx status", content: "Codex 状态", want: "blue"},
		{name: "model switch success", command: "/model gpt-5.2", content: "已将 Codex 模型切换为: gpt-5.2", want: "green"},
		{name: "reasoning switch success", command: "/reasoning high", content: "已将当前 Codex 会话推理强度切换为: high", want: "green"},
		{name: "model switch failure remains informational", command: "/model gpt-5.2", content: "切换模型失败，请重试。", want: "blue"},
		{name: "codex switch success", command: "/cx switch thread-1", content: "已切换并绑定。", want: "green"},
		{name: "workspace without sessions", command: "/cx cd @workspace-token", content: "当前工作空间没有可用会话。", want: "yellow"},
		{name: "claude switch success", command: "/cc switch session-1", content: "已切换 Claude 会话。", want: "green"},
		{name: "account switch success", command: "/cx account confirm token", content: "Codex 账号切换成功", want: "green"},
		{name: "runtime unavailable", command: "/cx switch thread-1", content: "已切换并绑定。\n运行通道: 暂不可用", want: "yellow"},
		{name: "timeout", command: "/cx switch thread-1", content: "会话切换等待超时，请重试。", want: "yellow"},
		{name: "switch failure", command: "/cx switch thread-1", content: "绑定 Codex 会话失败，请重试。", want: "red"},
		{name: "guide success", command: "/guide", content: "已发送到当前共享 Codex 任务。", want: "green"},
		{name: "cancel success", command: "/cancel", content: "已撤回该消息。", want: "green"},
		{name: "stop pending", command: "/stop", content: "已发送停止请求，等待任务终态。", want: "yellow"},
		{name: "stale task card", command: "/cancel", content: "该暂存消息已处理，或操作卡片已经过期。", want: "yellow"},
		{name: "task control failure", command: "/guide", content: "发送到当前共享 Codex 任务失败。", want: "red"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := choiceCommandResultTemplate(tt.command, tt.content); got != tt.want {
				t.Fatalf("template=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestTaskControlHandledCardKeepsContextualTitle(t *testing.T) {
	action := parsedCardAction{
		Choice: "/cancel", Kind: platform.ChoiceInteractionTaskControl, AgentName: "Codex",
	}
	card := buildSubmittedChoiceCard(action)
	data := card.Data.(map[string]any)
	title := data["header"].(map[string]any)["title"].(map[string]any)["content"]
	if title != "Codex · 暂存消息" {
		t.Fatalf("title=%#v, want contextual task control title", title)
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
