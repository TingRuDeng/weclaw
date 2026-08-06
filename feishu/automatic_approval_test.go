package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

type automaticApprovalRecorderForTest interface {
	RecordAutomaticApproval(context.Context, string, platform.Choice) error
}

func TestRecordAutomaticApprovalUpdatesTaskAndExistingPanel(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardIDs: []string{"card-task-1", "card-panel-1"}}
	reply := newReplierWithTaskCards(sender, "ou_user", cardKit, newTaskCardRegistry())
	if _, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Claude", InitialContent: "正在执行"}); err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}
	choice := automaticApprovalChoiceForTest("approval-1", "card-task-1")
	prompt := approvalPromptForTest("date")
	if err := reply.AskChoices(context.Background(), prompt, []platform.Choice{choice}); err != nil {
		t.Fatalf("AskChoices error: %v", err)
	}

	recorder := automaticApprovalRecorderFromReplierForTest(t, reply)
	if err := recorder.RecordAutomaticApproval(context.Background(), prompt, choice); err != nil {
		t.Fatalf("RecordAutomaticApproval error: %v", err)
	}

	if cardKit.updateCountFor("card-task-1") != 1 || cardKit.updateCountFor("card-panel-1") != 1 {
		t.Fatalf("updated card ids=%#v，期望任务卡和既有审批面板各更新一次", cardKit.updateCardIDs)
	}
	taskCard := cardKit.updateCards[0]
	if !strings.Contains(taskCard, "已自动批准（YOLO）") ||
		!strings.Contains(taskCard, "始终允许") || !strings.Contains(taskCard, "command: date") {
		t.Fatalf("task card=%s，期望记录 YOLO 自动批准、真实选项和操作摘要", taskCard)
	}
	panelCard := cardKit.updateCards[1]
	if !strings.Contains(panelCard, "已自动批准（YOLO）") || strings.Contains(panelCard, `"tag":"button"`) {
		t.Fatalf("panel card=%s，期望旧审批行进入无按钮终态", panelCard)
	}
}

func TestRecordAutomaticApprovalUpdatesStandaloneApprovalCard(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardID: "card-approval-1"}
	reply := NewReplier(sender, "ou_user", cardKit)
	choice := automaticApprovalChoiceForTest("approval-1", "")
	prompt := approvalPromptForTest("date")
	if err := reply.AskChoices(context.Background(), prompt, []platform.Choice{choice}); err != nil {
		t.Fatalf("AskChoices error: %v", err)
	}

	recorder := automaticApprovalRecorderFromReplierForTest(t, reply)
	if err := recorder.RecordAutomaticApproval(context.Background(), prompt, choice); err != nil {
		t.Fatalf("RecordAutomaticApproval error: %v", err)
	}

	if cardKit.updateCountFor("card-approval-1") != 1 {
		t.Fatalf("updated card ids=%#v，期望更新已发送的独立审批卡", cardKit.updateCardIDs)
	}
	card := cardKit.updateCards[0]
	if !strings.Contains(card, "已自动批准（YOLO）") || strings.Contains(card, `"tag":"button"`) {
		t.Fatalf("standalone card=%s，期望无按钮自动批准终态", card)
	}
}

func TestRecordAutomaticApprovalAddsTaskRecordWithoutCreatingApprovalCard(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardID: "card-task-1"}
	reply := newReplierWithTaskCards(sender, "ou_user", cardKit, newTaskCardRegistry())
	if _, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Claude", InitialContent: "正在执行"}); err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}
	choice := automaticApprovalChoiceForTest("approval-1", "card-task-1")

	recorder := automaticApprovalRecorderFromReplierForTest(t, reply)
	if err := recorder.RecordAutomaticApproval(context.Background(), approvalPromptForTest("date"), choice); err != nil {
		t.Fatalf("RecordAutomaticApproval error: %v", err)
	}

	if len(cardKit.createdCards) != 1 || len(sender.cards) != 1 {
		t.Fatalf("created=%d sent=%#v，YOLO 后续审批不应创建独立审批卡", len(cardKit.createdCards), sender.cards)
	}
	if cardKit.updateCountFor("card-task-1") != 1 || !strings.Contains(cardKit.updateCards[0], "已自动批准（YOLO）") {
		t.Fatalf("updates=%#v cards=%#v，期望只把自动批准写入任务卡", cardKit.updateCardIDs, cardKit.updateCards)
	}
}

func TestRecordAutomaticApprovalReturnsCardUpdateFailuresAfterAttemptingAllCards(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardIDs: []string{"card-task-1", "card-panel-1"}}
	reply := newReplierWithTaskCards(sender, "ou_user", cardKit, newTaskCardRegistry())
	if _, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Claude", InitialContent: "正在执行"}); err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}
	choice := automaticApprovalChoiceForTest("approval-1", "card-task-1")
	prompt := approvalPromptForTest("date")
	if err := reply.AskChoices(context.Background(), prompt, []platform.Choice{choice}); err != nil {
		t.Fatalf("AskChoices error: %v", err)
	}
	cardKit.updateErrors = []error{context.DeadlineExceeded, context.Canceled}

	recorder := automaticApprovalRecorderFromReplierForTest(t, reply)
	err := recorder.RecordAutomaticApproval(context.Background(), prompt, choice)

	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v，期望返回任务卡和审批面板更新失败", err)
	}
	if cardKit.updateCountFor("card-task-1") != 1 || cardKit.updateCountFor("card-panel-1") != 1 {
		t.Fatalf("updated card ids=%#v，单张卡失败不得阻止另一张卡更新尝试", cardKit.updateCardIDs)
	}
}

func automaticApprovalRecorderFromReplierForTest(t *testing.T, reply *Replier) automaticApprovalRecorderForTest {
	t.Helper()
	recorder, ok := any(reply).(automaticApprovalRecorderForTest)
	if !ok {
		t.Fatal("Replier 尚未实现自动审批卡片记录能力")
	}
	return recorder
}

func automaticApprovalChoiceForTest(key string, taskCardID string) platform.Choice {
	metadata := map[string]string{
		"approval_key":                         key,
		approvalOwnerValueKey:                  "ou_user",
		platform.ChoiceMetadataInteractionKind: platform.ChoiceInteractionApproval,
		platform.ChoiceMetadataAgentName:       "Claude",
	}
	if taskCardID != "" {
		metadata["task_card_id"] = taskCardID
	}
	return platform.Choice{ID: "allow_always", Label: "始终允许", Metadata: metadata}
}
