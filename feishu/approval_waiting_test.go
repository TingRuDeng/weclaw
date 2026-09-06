package feishu

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestPrivateApprovalWaitingLivesOnCurrentTaskCard(t *testing.T) {
	kit := &fakeCardKitClient{cardIDs: []string{"task", "panel"}}
	r := newReplierWithTaskCards(&fakeMessageSender{}, "oc_group", kit, newTaskCardRegistry())
	if _, err := r.OpenStream(context.Background(), platform.StreamOptions{Title: "Claude"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b"} {
		choice := automaticApprovalChoiceForTest(key, "task")
		choice.Metadata[approvalOwnerValueKey] = "ou_owner"
		choice.Metadata[feishuSessionMetadataKey] = "feishu:test:group:oc_group"
		if err := r.AskChoices(context.Background(), approvalPromptForTest("private-command"), []platform.Choice{choice}); err != nil {
			t.Fatal(err)
		}
	}
	opts, _ := r.taskCards.snapshot("task")
	card, _ := buildCardV2(opts)
	if !strings.Contains(card, "等待私聊审批（2 项）") || strings.Contains(card, "private-command") || len(opts.Approvals) != 0 {
		t.Fatalf("card=%s", card)
	}
	r.taskCards.record("next", cardOptions{Status: cardStatusStreaming, Title: "Claude"})
	r.BindTaskCard("next")
	r.taskCards.update("task", cardStatusSuperseded, "")
	for _, key := range []string{"a", "b"} {
		choice := automaticApprovalChoiceForTest(key, "task")
		if err := r.RecordApprovalTimeout(context.Background(), "", []platform.Choice{choice}); err != nil {
			t.Fatal(err)
		}
	}
	opts, _ = r.taskCards.snapshot("next")
	card, _ = buildCardV2(opts)
	if strings.Contains(card, "等待私聊审批") || len(opts.Approvals) != 2 {
		t.Fatalf("card=%s approvals=%v", card, opts.Approvals)
	}
	if kit.updateCountFor("next") != 3 {
		t.Fatalf("updates=%v", kit.updateCardIDs)
	}
}

func TestTerminalTaskDoesNotDisplayApprovalWaiting(t *testing.T) {
	registry := newTaskCardRegistry()
	registry.record("task", cardOptions{Status: cardStatusStreaming})
	registry.setApprovalWaiting("task", "request")
	opts, _ := registry.updateAndSnapshot("task", cardStatusDone, "完成", true)
	if opts.WaitingApprovals != 0 {
		t.Fatal("terminal task retained pending display state")
	}
	card, _ := buildCardV2(opts)
	if strings.Contains(card, "等待私聊审批") {
		t.Fatalf("card=%s", card)
	}
}

func TestApprovalCompletedBeforeWaitNoticeCannotReappear(t *testing.T) {
	registry := newTaskCardRegistry()
	registry.record("task", cardOptions{Status: cardStatusStreaming})
	registry.addApproval("task", parsedCardAction{Approval: "request", Choice: "deny", Status: approvalStatusHandled})
	if _, _, ok := registry.setApprovalWaiting("task", "request"); ok {
		t.Fatal("resolved request reappeared as pending")
	}
}

func TestApprovalWaitingMovesOnlyAfterBindingCommit(t *testing.T) {
	kit := &fakeCardKitClient{cardIDs: []string{"original", "candidate"}}
	r := newReplierWithTaskCards(&fakeMessageSender{}, "oc_group", kit, newTaskCardRegistry())
	for i := 0; i < 2; i++ {
		if _, err := r.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex"}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			r.taskCards.setApprovalWaiting("original", "request")
		}
	}
	before, _ := r.taskCards.snapshot("original")
	if before.WaitingApprovals != 1 || r.taskCards.approvalCardID("original") != "original" {
		t.Fatal("tentative stream creation moved the pending approval")
	}
	r.BindTaskCard("candidate")
	after, _ := r.taskCards.snapshot("candidate")
	if after.WaitingApprovals != 1 || r.taskCards.approvalCardID("original") != "candidate" {
		t.Fatal("committed binding did not move the pending approval")
	}
}
