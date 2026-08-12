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
	elements := card["body"].(map[string]any)["elements"].([]any)
	mainVisible, expandVisible, collapseVisible := false, false, false
	for _, raw := range card["body"].(map[string]any)["elements"].([]any) {
		element := raw.(map[string]any)
		switch element["element_id"] {
		case cardMainContentID:
			mainVisible = true
		case cardProgressExpandID:
			expandVisible = true
		case cardProgressCollapseID:
			collapseVisible = true
		}
	}
	if mainVisible || !expandVisible || collapseVisible {
		t.Fatalf("elements=%#v, want hidden progress and expand control only", elements)
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

func TestTaskProgressExpandActionUpdatesCardWithoutAgentDispatch(t *testing.T) {
	kit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = kit
	adapter.taskCards.record("card-task-1", cardOptions{
		Status: cardStatusStreaming, Title: "Codex", Summary: "摘要", Content: "完整时间线",
		Collapsible: true, Expanded: false, InlineActiveStatus: true,
	})
	durableRefreshes := 0
	adapter.taskCards.setDurableReferenceChangeHandler("card-task-1", func() { durableRefreshes++ })
	dispatched := make(chan struct{}, 1)
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_task"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			"action": "task_progress_expand", "task_card_id": "card-task-1",
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
	elements := card["body"].(map[string]any)["elements"].([]any)
	mainVisible, collapseVisible, expandVisible := false, false, false
	for _, raw := range elements {
		element := raw.(map[string]any)
		switch element["element_id"] {
		case cardMainContentID:
			mainVisible = true
		case cardProgressCollapseID:
			collapseVisible = true
		case cardProgressExpandID:
			expandVisible = true
		}
	}
	if !mainVisible || !collapseVisible || expandVisible {
		t.Fatalf("elements=%#v, want details and collapse control only", elements)
	}
	opts, ok := adapter.taskCards.snapshot("card-task-1")
	if !ok || !opts.Expanded {
		t.Fatalf("task card state=%#v, want expanded", opts)
	}
	if durableRefreshes != 1 {
		t.Fatalf("durable reference refreshes=%d, want 1 after CardKit update", durableRefreshes)
	}
	select {
	case <-dispatched:
		t.Fatal("progress expand action reached Agent dispatch")
	default:
	}
}

func TestCollapsedTaskCardAccumulatesProgressUntilExpanded(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-task-1"}
	registry := newTaskCardRegistry()
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, registry)
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title:               "Codex",
		InitialPresentation: &platform.StreamPresentation{Summary: "第一步", Details: "第一步详情"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream.(*feishuStream).throttle = 0
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = kit
	adapter.taskCards = registry
	dispatch := func(context.Context, platform.IncomingMessage, platform.Replier) {
		t.Fatal("progress control reached Agent dispatch")
	}
	control := func(action string) {
		t.Helper()
		event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_task"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action": action, "task_card_id": "card-task-1",
			}},
		}}
		response, actionErr := adapter.handleCardActionEvent(context.Background(), event, dispatch)
		if actionErr != nil || response == nil || response.Toast == nil || response.Toast.Type != "success" {
			t.Fatalf("action=%s response=%#v err=%v", action, response, actionErr)
		}
	}

	control("task_progress_collapse")
	streamCallsBefore := len(kit.streamElementIDs)
	if err := stream.(platform.StructuredProgressStream).UpdatePresentation(context.Background(), platform.StreamPresentation{
		Summary: "第二步", Details: "第一步详情\n\n第二步详情",
	}); err != nil {
		t.Fatal(err)
	}
	if len(kit.streamElementIDs) != streamCallsBefore {
		t.Fatalf("collapsed card streamed hidden progress: before=%d after=%d elements=%#v",
			streamCallsBefore, len(kit.streamElementIDs), kit.streamElementIDs)
	}
	if err := stream.Update(context.Background(), "第一步详情\n\n第二步详情\n\n第三步详情"); err != nil {
		t.Fatal(err)
	}
	if len(kit.streamElementIDs) != streamCallsBefore {
		t.Fatalf("collapsed card streamed hidden plain progress: before=%d after=%d elements=%#v",
			streamCallsBefore, len(kit.streamElementIDs), kit.streamElementIDs)
	}
	control("task_progress_expand")
	card := decodeCardJSON(t, kit.updateCards[len(kit.updateCards)-1])
	elements := card["body"].(map[string]any)["elements"].([]any)
	mainContent, summaryVisible := "", false
	for _, raw := range elements {
		element := raw.(map[string]any)
		switch element["element_id"] {
		case cardMainContentID:
			mainContent, _ = element["content"].(string)
		case "progress_summary":
			summaryVisible = true
		}
	}
	if mainContent != "第一步详情\n\n第二步详情\n\n第三步详情" || summaryVisible {
		t.Fatalf("expanded elements=%#v, want latest complete progress without summary duplicate", elements)
	}
}
