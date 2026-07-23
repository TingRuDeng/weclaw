package feishu

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestHandleCardActionEventUpdatesMappedTaskCard(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	adapter.taskCards.record("card-task-1", cardOptions{
		Status:  cardStatusThinking,
		Title:   "Codex",
		Content: "正在分析任务",
	})
	now := time.Date(2026, 7, 3, 8, 31, 38, 874000000, time.Local)
	adapter.now = func() time.Time { return now }
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card", resp)
	}
	assertApprovalCardContent(t, resp, "✅ 已收纳到任务卡片")
	assertApprovalCardNotContains(t, resp, "command: date")
	if cardKit.updateCountFor("card-task-1") != 1 {
		t.Fatalf("updated card ids=%#v, want task card update", cardKit.updateCardIDs)
	}
	if cardKit.updateSeqs[0] != 1 {
		t.Fatalf("update sequence=%d, want task card sequence 1", cardKit.updateSeqs[0])
	}
	select {
	case msg := <-dispatched:
		if msg.RawCommand.Value["approval_key"] != "approval-key-1" ||
			msg.RawCommand.Value[platform.ChoiceMetadataInteractionKind] != platform.ChoiceInteractionApproval {
			t.Fatalf("raw command=%#v, want approval key passthrough", msg.RawCommand.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleApprovalCardActionPreservesSessionKey(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := approvalCardActionEvent("allow", "允许本次", "")
	sessionKey := "feishu:tenant_1:dm:oc_1:ou_user"
	event.Event.Action.Value[feishuSessionMetadataKey] = sessionKey
	dispatched := make(chan platform.IncomingMessage, 1)

	if _, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	}); err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}

	select {
	case msg := <-dispatched:
		if got := msg.Metadata[feishuSessionMetadataKey]; got != sessionKey {
			t.Fatalf("metadata=%#v, want session key %q", msg.Metadata, sessionKey)
		}
		if got := msg.SessionRouteKey(); got != sessionKey {
			t.Fatalf("route=%q, want session key %q", got, sessionKey)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleCardActionEventAppendsApprovalToTaskCardState(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	adapter.taskCards.record("card-task-1", cardOptions{
		Status:  cardStatusThinking,
		Title:   "Codex",
		Content: "正在分析任务",
	})
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card", resp)
	}
	assertApprovalCardContent(t, resp, "✅ 已收纳到任务卡片")
	assertApprovalCardNotContains(t, resp, "command: date")
	if cardKit.updateCountFor("card-task-1") != 1 {
		t.Fatalf("updated card ids=%#v, want task card update", cardKit.updateCardIDs)
	}
	card := decodeCardJSON(t, cardKit.updateCards[0])
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	main := elements[1].(map[string]any)
	approval := elements[2].(map[string]any)
	if main["content"] != "正在分析任务" {
		t.Fatalf("main content=%#v, want preserved task content", main["content"])
	}
	if !strings.Contains(approval["content"].(string), "允许本次") {
		t.Fatalf("approval content=%q, want approval label", approval["content"])
	}
	select {
	case msg := <-dispatched:
		if msg.RawCommand.Value["task_card_id"] != "card-task-1" {
			t.Fatalf("raw command=%#v, want task card passthrough", msg.RawCommand.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleCardActionEventUpdatesApprovalPanelCard(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	adapter.taskCards.record("card-task-1", cardOptions{
		Status:  cardStatusThinking,
		Title:   "Codex",
		Content: "正在分析任务",
	})
	item := approvalPanelItem{
		Key:      "approval-key-1",
		Summary:  "command: date",
		TaskCard: "card-task-1",
		Choices:  []approvalPanelChoice{{ID: "allow", Label: "允许本次", Conv: "feishu:ou_user"}},
	}
	if _, ok := adapter.taskCards.upsertApprovalPanelItem("card-task-1", item); !ok {
		t.Fatal("approval panel item should be registered")
	}
	if _, ok := adapter.taskCards.bindApprovalPanelCard("card-task-1", "card-panel-1"); !ok {
		t.Fatal("approval panel card should be bound")
	}
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	event.Event.Action.Value["approval_panel"] = "1"
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	assertApprovalCardContent(t, resp, "本轮审批已处理", "记录已收纳到任务卡片")
	assertApprovalCardNotContains(t, resp, "待处理审批")
	assertApprovalPanelElementCount(t, resp, 1)
	if cardKit.updateCountFor("card-task-1") != 1 {
		t.Fatalf("updated card ids=%#v, want task card update", cardKit.updateCardIDs)
	}
	select {
	case msg := <-dispatched:
		if msg.RawCommand.Value["approval_key"] != "approval-key-1" {
			t.Fatalf("raw command=%#v, want approval key", msg.RawCommand.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleCardActionEventKeepsPanelRecordWhenTaskCardUpdateFails(t *testing.T) {
	cardKit := &fakeCardKitClient{updateErrors: []error{context.Canceled}}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	adapter.taskCards.record("card-task-1", cardOptions{
		Status:  cardStatusThinking,
		Title:   "Codex",
		Content: "正在分析任务",
	})
	item := approvalPanelItem{
		Key:      "approval-key-1",
		Summary:  "command: date",
		TaskCard: "card-task-1",
		Choices:  []approvalPanelChoice{{ID: "allow", Label: "允许本次", Conv: "feishu:ou_user"}},
	}
	if _, ok := adapter.taskCards.upsertApprovalPanelItem("card-task-1", item); !ok {
		t.Fatal("approval panel item should be registered")
	}
	if _, ok := adapter.taskCards.bindApprovalPanelCard("card-task-1", "card-panel-1"); !ok {
		t.Fatal("approval panel card should be bound")
	}
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	event.Event.Action.Value["approval_panel"] = "1"
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	assertApprovalCardContent(t, resp, "已处理审批：1 个")
	assertApprovalCardAllContent(t, resp, "✅ 已授权：允许本次", "command: date")
	if cardKit.updateCountFor("card-task-1") != 1 {
		t.Fatalf("updated card ids=%#v, want attempted task card update", cardKit.updateCardIDs)
	}
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestApprovalPanelShowsWarningForUnconfirmedResult(t *testing.T) {
	snapshot := approvalPanelSnapshot{Items: []approvalPanelItem{{
		Key: "approval-key-1", Choice: "allow", Label: "允许本次", Status: approvalStatusUnconfirmed,
	}}}
	card := buildApprovalPanelCardData(snapshot)
	header := card["header"].(map[string]any)
	if got := header["template"]; got != "yellow" {
		t.Fatalf("template=%v, want warning", got)
	}
	content := approvalPanelTitle(snapshot) + "\n" + approvalPanelItemStatus(snapshot.Items[0])
	if !strings.Contains(content, "1 个结果未确认或已过期") || !strings.Contains(content, "处理结果未确认") {
		t.Fatalf("content=%q, want explicit unconfirmed warning", content)
	}
}

func TestHandleCardActionEventIgnoresTaskCardUpdateFailure(t *testing.T) {
	cardKit := &fakeCardKitClient{updateErrors: []error{context.Canceled}}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card despite task card failure", resp)
	}
	assertApprovalCardContent(t, resp, "✅ 已授权", "允许本次")
	assertApprovalCardNotContains(t, resp, "command: date")
	assertApprovalCardNotContains(t, resp, "已收纳到任务卡片")
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}

func TestHandleCardActionEventDoesNotOverwriteUnknownTaskCard(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	event := approvalCardActionEvent("allow", "允许本次", "card-task-1")
	dispatched := make(chan platform.IncomingMessage, 1)

	resp, err := adapter.handleCardActionEvent(context.Background(), event, func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
		dispatched <- msg
		consumeApprovalForTest(msg)
	})

	if err != nil {
		t.Fatalf("handleCardActionEvent error: %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("response=%#v, want compact approval card", resp)
	}
	if cardKit.updateCountFor("card-task-1") != 0 {
		t.Fatalf("updated card ids=%#v, want no task card overwrite without registry state", cardKit.updateCardIDs)
	}
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback dispatch")
	}
}
