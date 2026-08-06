package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

type standaloneApprovalCard struct {
	cardID   string
	sequence int
}

// RecordAutomaticApproval 把 YOLO 的真实允许决策写回现有审批卡和任务卡；展示失败不会改变审批真值。
func (r *Replier) RecordAutomaticApproval(ctx context.Context, prompt string, choice platform.Choice) error {
	if r == nil || r.cardKit == nil {
		return platform.ErrUnsupported
	}
	action, ok := automaticApprovalAction(prompt, choice)
	if !ok {
		return platform.ErrUnsupported
	}
	handled := false
	failures := make([]error, 0, 3)

	if updated, err := r.recordAutomaticApprovalOnTaskCard(ctx, action); updated {
		handled = true
		if err != nil {
			failures = append(failures, err)
		}
	}
	if updated, err := r.recordAutomaticApprovalOnPanel(ctx, action); updated {
		handled = true
		if err != nil {
			failures = append(failures, err)
		}
	}
	if updated, err := r.recordAutomaticApprovalOnStandaloneCard(ctx, action); updated {
		handled = true
		if err != nil {
			failures = append(failures, err)
		}
	}
	if !handled {
		return platform.ErrUnsupported
	}
	return errors.Join(failures...)
}

func automaticApprovalAction(prompt string, choice platform.Choice) (parsedCardAction, bool) {
	choiceID := strings.TrimSpace(choice.ID)
	approvalKey := strings.TrimSpace(choice.Metadata["approval_key"])
	taskCardID := strings.TrimSpace(choice.Metadata["task_card_id"])
	if choiceID == "" || (approvalKey == "" && taskCardID == "") {
		return parsedCardAction{}, false
	}
	return parsedCardAction{
		Action:   cardActionChoice,
		Choice:   choiceID,
		Kind:     cardKindApproval,
		Label:    strings.TrimSpace(choice.Label),
		Summary:  approvalSummaryFromPrompt(prompt),
		TaskCard: taskCardID,
		Approval: approvalKey,
		Owner:    strings.TrimSpace(choice.Metadata[approvalOwnerValueKey]),
		Status:   approvalStatusAutoApproved,
	}, true
}

func (r *Replier) recordAutomaticApprovalOnTaskCard(ctx context.Context, action parsedCardAction) (bool, error) {
	if r.taskCards == nil || strings.TrimSpace(action.TaskCard) == "" {
		return false, nil
	}
	opts, sequence, ok := r.taskCards.addApprovalWithSequence(action.TaskCard, action)
	if !ok {
		return false, nil
	}
	cardJSON, err := buildCardV2(opts)
	if err != nil {
		return true, fmt.Errorf("build automatic approval task card: %w", err)
	}
	if err := r.cardKit.UpdateCard(ctx, action.TaskCard, cardJSON, sequence); err != nil {
		return true, fmt.Errorf("update automatic approval task card: %w", err)
	}
	return true, nil
}

func (r *Replier) recordAutomaticApprovalOnPanel(ctx context.Context, action parsedCardAction) (bool, error) {
	if r.taskCards == nil {
		return false, nil
	}
	snapshot, ok := r.taskCards.completeApprovalPanelItem(action)
	if !ok || strings.TrimSpace(snapshot.CardID) == "" {
		return false, nil
	}
	cardJSON, err := buildApprovalPanelCardJSON(snapshot)
	if err != nil {
		return true, fmt.Errorf("build automatic approval panel card: %w", err)
	}
	if err := r.cardKit.UpdateCard(ctx, snapshot.CardID, cardJSON, snapshot.Seq); err != nil {
		return true, fmt.Errorf("update automatic approval panel card: %w", err)
	}
	return true, nil
}

func (r *Replier) recordAutomaticApprovalOnStandaloneCard(ctx context.Context, action parsedCardAction) (bool, error) {
	key := strings.TrimSpace(action.Approval)
	card, ok := r.nextStandaloneApprovalCard(key)
	if !ok {
		return false, nil
	}
	cardJSON, err := json.Marshal(buildChoiceHandledCard(action).Data)
	if err != nil {
		return true, fmt.Errorf("marshal automatic approval card: %w", err)
	}
	if err := r.cardKit.UpdateCard(ctx, card.cardID, string(cardJSON), card.sequence); err != nil {
		return true, fmt.Errorf("update automatic approval card: %w", err)
	}
	r.forgetStandaloneApprovalCard(key, card.cardID)
	return true, nil
}

func (r *Replier) rememberStandaloneApprovalCard(prompt string, choices []platform.Choice, conv string, cardID string) {
	options := choiceOptions(prompt, choices, conv)
	if options.Kind != cardKindApproval || strings.TrimSpace(cardID) == "" {
		return
	}
	key := ""
	for _, choice := range choices {
		if key = strings.TrimSpace(choice.Metadata["approval_key"]); key != "" {
			break
		}
	}
	if key == "" {
		return
	}
	r.approvalMu.Lock()
	defer r.approvalMu.Unlock()
	if r.approvalCard == nil {
		r.approvalCard = make(map[string]standaloneApprovalCard)
	}
	r.approvalCard[key] = standaloneApprovalCard{cardID: strings.TrimSpace(cardID)}
}

func (r *Replier) nextStandaloneApprovalCard(key string) (standaloneApprovalCard, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return standaloneApprovalCard{}, false
	}
	r.approvalMu.Lock()
	defer r.approvalMu.Unlock()
	card, ok := r.approvalCard[key]
	if !ok || strings.TrimSpace(card.cardID) == "" {
		return standaloneApprovalCard{}, false
	}
	card.sequence++
	r.approvalCard[key] = card
	return card, true
}

func (r *Replier) forgetStandaloneApprovalCard(key string, cardID string) {
	r.approvalMu.Lock()
	defer r.approvalMu.Unlock()
	if card, ok := r.approvalCard[key]; ok && card.cardID == cardID {
		delete(r.approvalCard, key)
	}
}
