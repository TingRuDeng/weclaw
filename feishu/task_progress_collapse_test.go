package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestTaskProgressCollapseActionUpdatesCardWithoutAgentDispatch(t *testing.T) {
	kit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = kit
	adapter.taskCards.record("card-task-1", cardOptions{
		Status: cardStatusStreaming, Title: "Codex", Summary: "摘要", Preview: "最近五条", Content: "完整时间线",
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
	if !mainVisible || !expandVisible || collapseVisible {
		t.Fatalf("elements=%#v, want preview and expand control only", elements)
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
		Status: cardStatusStreaming, Title: "Codex", Summary: "摘要", Preview: "最近五条", Content: "完整时间线",
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

func TestTaskProgressExpandFailureKeepsCollapsedRegistryState(t *testing.T) {
	kit := &fakeCardKitClient{updateErrors: []error{errors.New("update failed")}}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = kit
	adapter.taskCards.record("card-task-1", cardOptions{
		Status: cardStatusStreaming, Title: "Codex", Preview: "最近五条", Content: "完整时间线",
		Collapsible: true, Expanded: false, InlineActiveStatus: true,
	})
	durableRefreshes := 0
	adapter.taskCards.setDurableReferenceChangeHandler("card-task-1", func() { durableRefreshes++ })
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_task"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			"action": "task_progress_expand", "task_card_id": "card-task-1",
		}},
	}}

	response, err := adapter.handleCardActionEvent(context.Background(), event, func(context.Context, platform.IncomingMessage, platform.Replier) {})
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Toast == nil || response.Toast.Type != "warning" {
		t.Fatalf("response=%#v, want warning toast", response)
	}
	opts, ok := adapter.taskCards.snapshot("card-task-1")
	if !ok || opts.Expanded {
		t.Fatalf("task card state=%#v, want collapsed after remote update failure", opts)
	}
	if durableRefreshes != 0 {
		t.Fatalf("durable reference refreshes=%d, want 0 after failed update", durableRefreshes)
	}
}

func TestTaskCardStreamsPreviewThenExpandedDetailsInOneBody(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-task-1"}
	registry := newTaskCardRegistry()
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, registry)
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title:               "Codex",
		InitialPresentation: &platform.StreamPresentation{Summary: "第五步", Preview: "第三步\n\n第四步\n\n第五步", Details: "第一步\n\n第二步\n\n第三步\n\n第四步\n\n第五步"},
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

	opts, ok := registry.snapshot("card-task-1")
	if !ok || opts.Expanded {
		t.Fatalf("initial task card=%#v, want collapsed preview", opts)
	}
	streamCallsBefore := len(kit.streamElementIDs)
	if err := stream.(platform.StructuredProgressStream).UpdatePresentation(context.Background(), platform.StreamPresentation{
		Summary: "第六步", Preview: "第二步\n\n第三步\n\n第四步\n\n第五步\n\n第六步", Details: "第一步\n\n第二步\n\n第三步\n\n第四步\n\n第五步\n\n第六步",
	}); err != nil {
		t.Fatal(err)
	}
	if len(kit.streamElementIDs) != streamCallsBefore+1 || kit.streamTexts[len(kit.streamTexts)-1] != "第二步\n\n第三步\n\n第四步\n\n第五步\n\n第六步" {
		t.Fatalf("collapsed card did not stream latest preview: before=%d after=%d texts=%#v elements=%#v",
			streamCallsBefore, len(kit.streamElementIDs), kit.streamTexts, kit.streamElementIDs)
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
	if mainContent != "第一步\n\n第二步\n\n第三步\n\n第四步\n\n第五步\n\n第六步" || summaryVisible {
		t.Fatalf("expanded elements=%#v, want latest complete progress without summary duplicate", elements)
	}
	if err := stream.(platform.StructuredProgressStream).UpdatePresentation(context.Background(), platform.StreamPresentation{
		Summary: "第七步", Preview: "第三步\n\n第四步\n\n第五步\n\n第六步\n\n第七步", Details: "第一步\n\n第二步\n\n第三步\n\n第四步\n\n第五步\n\n第六步\n\n第七步",
	}); err != nil {
		t.Fatal(err)
	}
	if got := kit.streamTexts[len(kit.streamTexts)-1]; got != "第一步\n\n第二步\n\n第三步\n\n第四步\n\n第五步\n\n第六步\n\n第七步" {
		t.Fatalf("expanded stream=%q, want complete details", got)
	}
	control("task_progress_collapse")
	card = decodeCardJSON(t, kit.updateCards[len(kit.updateCards)-1])
	elements = card["body"].(map[string]any)["elements"].([]any)
	mainContent = ""
	for _, raw := range elements {
		element := raw.(map[string]any)
		if element["element_id"] == cardMainContentID {
			mainContent, _ = element["content"].(string)
		}
	}
	if mainContent != "第三步\n\n第四步\n\n第五步\n\n第六步\n\n第七步" {
		t.Fatalf("collapsed elements=%#v, want latest preview", elements)
	}
}

func TestTerminalTaskCardAutomaticallyCollapsesAndKeepsProgressControls(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-task-1"}
	registry := newTaskCardRegistry()
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, registry)
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex",
		InitialPresentation: &platform.StreamPresentation{
			Summary: "第六条说明",
			Preview: "第二条说明\n\n第三条说明\n\n第四条说明\n\n第五条说明\n\n第六条说明",
			Details: "第一条说明\n\n第二条说明\n\n第三条说明\n\n第四条说明\n\n第五条说明\n\n第六条说明",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = kit
	adapter.taskCards = registry
	dispatch := func(context.Context, platform.IncomingMessage, platform.Replier) {
		t.Fatal("progress control reached Agent dispatch")
	}
	control := func(action string) *callback.CardActionTriggerResponse {
		t.Helper()
		event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_task"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				"action": action, "task_card_id": "card-task-1",
			}},
		}}
		response, actionErr := adapter.handleCardActionEvent(context.Background(), event, dispatch)
		if actionErr != nil {
			t.Fatal(actionErr)
		}
		return response
	}

	if response := control("task_progress_expand"); response.Toast == nil || response.Toast.Type != "success" {
		t.Fatalf("expand response=%#v", response)
	}
	if err := stream.Complete(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	opts, ok := registry.snapshot("card-task-1")
	if !ok || opts.Expanded || !opts.Collapsible {
		t.Fatalf("terminal card=%#v, want collapsed progress", opts)
	}
	terminalCard := decodeCardJSON(t, kit.updateCards[len(kit.updateCards)-1])
	terminalElements := terminalCard["body"].(map[string]any)["elements"].([]any)
	if taskProgressElementContent(terminalElements, cardMainContentID) != opts.Preview ||
		taskProgressElementContent(terminalElements, cardProgressExpandID) == "" {
		t.Fatalf("terminal elements=%#v, want preview and expand button", terminalElements)
	}

	if response := control("task_progress_expand"); response.Toast == nil || response.Toast.Type != "success" {
		t.Fatalf("terminal expand response=%#v", response)
	}
	expanded := decodeCardJSON(t, kit.updateCards[len(kit.updateCards)-1])
	expandedElements := expanded["body"].(map[string]any)["elements"].([]any)
	if !strings.Contains(taskProgressElementContent(expandedElements, cardMainContentID), "第一条说明") ||
		taskProgressElementContent(expandedElements, cardProgressCollapseID) == "" {
		t.Fatalf("expanded elements=%#v, want complete progress and collapse button", expandedElements)
	}
	if response := control("task_progress_collapse"); response.Toast == nil || response.Toast.Type != "success" {
		t.Fatalf("terminal collapse response=%#v", response)
	}
	collapsed := decodeCardJSON(t, kit.updateCards[len(kit.updateCards)-1])
	collapsedElements := collapsed["body"].(map[string]any)["elements"].([]any)
	if strings.Contains(taskProgressElementContent(collapsedElements, cardMainContentID), "第一条说明") ||
		taskProgressElementContent(collapsedElements, cardProgressExpandID) == "" {
		t.Fatalf("collapsed elements=%#v, want latest-five preview and expand button", collapsedElements)
	}
}

func taskProgressElementContent(elements []any, elementID string) string {
	for _, raw := range elements {
		element, _ := raw.(map[string]any)
		if element["element_id"] != elementID {
			continue
		}
		if content, ok := element["content"].(string); ok {
			return content
		}
		if text, ok := element["text"].(map[string]any); ok {
			content, _ := text["content"].(string)
			return content
		}
	}
	return ""
}
