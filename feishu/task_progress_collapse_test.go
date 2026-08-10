package feishu

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestTaskProgressCollapseActionUpdatesCardWithoutAgentDispatch(t *testing.T) {
	kit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = kit
	adapter.taskCards.record("card-task-1", cardOptions{
		Status: cardStatusStreaming, Title: "Codex", Summary: "摘要", Content: "完整时间线",
		Collapsible: true, Expanded: true, InlineActiveStatus: true,
	})
	durableRefreshes := 0
	adapter.taskCards.setDurableReferenceChangeHandler("card-task-1", func() { durableRefreshes++ })
	dispatched := make(chan struct{}, 1)
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_task"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			"action": "task_progress_collapse", "task_card_id": "card-task-1",
		}},
	}}
	response, err := adapter.handleCardActionEvent(context.Background(), event, func(context.Context, platform.IncomingMessage, platform.Replier) {
		dispatched <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Toast == nil || response.Toast.Type != "success" {
		t.Fatalf("response=%#v, want success toast", response)
	}
	if len(kit.updateCards) != 1 || kit.updateCardIDs[0] != "card-task-1" {
		t.Fatalf("updated cards=%#v ids=%#v, want one task-card update", kit.updateCards, kit.updateCardIDs)
	}
	card := decodeCardJSON(t, kit.updateCards[0])
	for _, raw := range card["body"].(map[string]any)["elements"].([]any) {
		element := raw.(map[string]any)
		if element["element_id"] == cardProgressPanelID && element["expanded"] != false {
			t.Fatalf("progress panel=%#v, want collapsed", element)
		}
	}
	opts, ok := adapter.taskCards.snapshot("card-task-1")
	if !ok || opts.Expanded {
		t.Fatalf("task card state=%#v, want collapsed", opts)
	}
	if durableRefreshes != 1 {
		t.Fatalf("durable reference refreshes=%d, want 1 after CardKit update", durableRefreshes)
	}
	select {
	case <-dispatched:
		t.Fatal("progress collapse action reached Agent dispatch")
	default:
	}
}
