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

func TestBuildChoiceCardLabelsClaudeApprovalSource(t *testing.T) {
	cardJSON, err := buildChoiceCard("Claude 请求执行敏感操作，请确认：\n\n{\"cmd\":\"date\"}", []platform.Choice{{
		ID: "allow", Label: "允许本次", Metadata: map[string]string{
			platform.ChoiceMetadataInteractionKind: platform.ChoiceInteractionApproval,
			platform.ChoiceMetadataAgentName:       "Claude",
		},
	}}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	card := decodeCardJSON(t, cardJSON)
	header := card["header"].(map[string]any)
	title := header["title"].(map[string]any)
	if title["content"] != "Claude 授权" {
		t.Fatalf("title=%#v，期望 Claude 授权", title["content"])
	}
	body := card["body"].(map[string]any)
	value := body["elements"].([]any)[1].(map[string]any)["value"].(map[string]any)
	if value["kind"] != cardKindApproval || !strings.Contains(value["summary"].(string), "date") {
		t.Fatalf("value=%#v，期望保留 Claude 授权摘要", value)
	}
}

func TestBuildChoiceCardExplicitUserInputKindOverridesPromptText(t *testing.T) {
	cardJSON, err := buildChoiceCard("是否允许？请求执行敏感操作，请确认：", []platform.Choice{{
		ID: "继续", Label: "继续", Metadata: map[string]string{
			"approval_key":                         "question-1",
			platform.ChoiceMetadataInteractionKind: platform.ChoiceInteractionUserInput,
			platform.ChoiceMetadataAgentName:       "Claude",
		},
	}}, "feishu:ou_user")
	if err != nil {
		t.Fatal(err)
	}
	card := decodeCardJSON(t, cardJSON)
	header := card["header"].(map[string]any)["title"].(map[string]any)
	if header["content"] != "Claude 提问" {
		t.Fatalf("title=%#v，显式 user_input 不应按授权卡处理", header["content"])
	}
	value := card["body"].(map[string]any)["elements"].([]any)[1].(map[string]any)["value"].(map[string]any)
	if value["kind"] != platform.ChoiceInteractionUserInput || value["summary"] != nil {
		t.Fatalf("value=%#v，显式交互类型应优先于 prompt 文案", value)
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

func TestBuildChoiceCardUsesFeishuSessionMetadata(t *testing.T) {
	cardJSON, err := buildChoiceCard("请选择工作空间", []platform.Choice{{
		ID:    "/cx cd 0",
		Label: "weclaw",
		Metadata: map[string]string{
			"feishu_session_key": "feishu:tenant_1:group:oc_1:om_root",
			modelSettingAgentKey: "claude",
		},
	}}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	card := decodeCardJSON(t, cardJSON)
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	value := elements[1].(map[string]any)["value"].(map[string]any)
	if value["feishu_session_key"] != "feishu:tenant_1:group:oc_1:om_root" {
		t.Fatalf("button value=%#v, want feishu session metadata", value)
	}
	if value[modelSettingAgentKey] != "claude" {
		t.Fatalf("button value=%#v, want model setting agent", value)
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

func TestBuildChoiceCardSeparatesNavigationButtons(t *testing.T) {
	cardJSON, err := buildChoiceCard("请选择会话", []platform.Choice{
		{ID: "/cx switch thread-1", Label: "会话 1"},
		{ID: "/cx cd ..", Label: "← 返回上一级", Metadata: map[string]string{
			platform.ChoiceMetadataButtonType: platform.ChoiceButtonTypeDefault,
			platform.ChoiceMetadataSection:    platform.ChoiceSectionNavigation,
		}},
	}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	body := decodeCardJSON(t, cardJSON)["body"].(map[string]any)
	elements := body["elements"].([]any)
	if elements[2].(map[string]any)["tag"] != "hr" {
		t.Fatalf("elements=%#v，导航按钮前应有分隔线", elements)
	}
	button := elements[3].(map[string]any)
	if button["type"] != platform.ChoiceButtonTypeDefault {
		t.Fatalf("button=%#v，返回按钮应使用次级样式", button)
	}
}

func TestParseCardAction(t *testing.T) {
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user", UserID: stringPtr("user_1")},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_msg"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action":             cardActionChoice,
				"choice":             "2",
				"conv":               "feishu:ou_user",
				"feishu_session_key": "feishu:tenant_1:group:oc_1:om_root",
				modelSettingAgentKey: "claude",
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
	if action.SessionKey != "feishu:tenant_1:group:oc_1:om_root" {
		t.Fatalf("action=%#v, want feishu session key", action)
	}
	if action.AgentName != "claude" {
		t.Fatalf("action=%#v, want model setting agent", action)
	}
	if action.UserID != "ou_user" || action.ChatID != "oc_chat" || action.MessageID != "om_msg" {
		t.Fatalf("action=%#v, want operator and context ids", action)
	}
	if !containsString(action.UserAliases, "user_1") {
		t.Fatalf("aliases=%#v, want callback user_id", action.UserAliases)
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
	if !strings.Contains(content, "✅ 已授权") || !strings.Contains(content, "允许本次") {
		t.Fatalf("content=%q, want compact allow status", content)
	}
	if strings.Contains(content, "command: date") || strings.Contains(content, "{") || strings.Contains(content, "}") || len(body["elements"].([]map[string]any)) != 1 {
		t.Fatalf("content=%q, want compact card without verbose JSON or buttons", content)
	}
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}
