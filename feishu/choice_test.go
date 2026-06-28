package feishu

import (
	"encoding/json"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestBuildChoiceCardIncludesButtonValues(t *testing.T) {
	cardJSON, err := buildChoiceCard("请选择", []platform.Choice{{ID: "1", Label: "继续"}}, "feishu:ou_user")
	if err != nil {
		t.Fatalf("buildChoiceCard error: %v", err)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("card json invalid: %v", err)
	}
	elements := card["elements"].([]any)
	action := elements[1].(map[string]any)
	button := action["actions"].([]any)[0].(map[string]any)
	value := button["value"].(map[string]any)
	if value["action"] != cardActionChoice || value["choice"] != "1" || value["conv"] != "feishu:ou_user" {
		t.Fatalf("button value=%#v, want choice payload", value)
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
