package feishu

import (
	"strings"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func approvalCardActionEvent(choice string, label string, taskCardID string) *callback.CardActionTriggerEvent {
	value := map[string]interface{}{
		"action":         cardActionChoice,
		"choice":         choice,
		"kind":           cardKindApproval,
		"label":          label,
		"summary":        "command: date",
		"approval_key":   "approval-key-1",
		"approval_owner": "ou_user",
	}
	if taskCardID != "" {
		value["task_card_id"] = taskCardID
	}
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_msg"},
			Action:   &callback.CallBackAction{Value: value},
		},
	}
}

func assertApprovalCardContent(t *testing.T, resp *callback.CardActionTriggerResponse, wants ...string) {
	t.Helper()
	content := approvalCardContentForTest(t, resp)
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Fatalf("content=%q, want %q", content, want)
		}
	}
}

func assertApprovalCardNotContains(t *testing.T, resp *callback.CardActionTriggerResponse, forbidden ...string) {
	t.Helper()
	content := approvalCardContentForTest(t, resp)
	for _, value := range forbidden {
		if strings.Contains(content, value) {
			t.Fatalf("content=%q, should not contain %q", content, value)
		}
	}
}

func assertApprovalCardAllContent(t *testing.T, resp *callback.CardActionTriggerResponse, wants ...string) {
	t.Helper()
	content := approvalCardAllContentForTest(t, resp)
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Fatalf("content=%q, want %q", content, want)
		}
	}
}

func assertApprovalPanelElementCount(t *testing.T, resp *callback.CardActionTriggerResponse, want int) {
	t.Helper()
	elements := approvalCardElementsForTest(t, resp)
	if len(elements) != want {
		t.Fatalf("elements=%d, want %d", len(elements), want)
	}
}

func approvalCardContentForTest(t *testing.T, resp *callback.CardActionTriggerResponse) string {
	t.Helper()
	elements := approvalCardElementsForTest(t, resp)
	return elements[0]["content"].(string)
}

func approvalCardAllContentForTest(t *testing.T, resp *callback.CardActionTriggerResponse) string {
	t.Helper()
	elements := approvalCardElementsForTest(t, resp)
	parts := make([]string, 0, len(elements))
	for _, element := range elements {
		if content, ok := element["content"].(string); ok {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n")
}

func approvalCardElementsForTest(t *testing.T, resp *callback.CardActionTriggerResponse) []map[string]any {
	t.Helper()
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card", resp)
	}
	card := resp.Card.Data.(map[string]any)
	body := card["body"].(map[string]any)
	return body["elements"].([]map[string]any)
}
