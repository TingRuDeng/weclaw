package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type standaloneApprovalCard struct {
	cardID   string
	sequence int
}

// RecordApprovalState 在 Desktop 权威复核确认 request 已由 App 处理或原 turn
// 已结束时，原地关闭现有飞书审批展示；展示失败不会改变审批真值。
func (r *Replier) RecordApprovalState(ctx context.Context, prompt string, choices []platform.Choice, state agent.ApprovalRequestState) error {
	if r == nil || r.cardKit == nil || len(choices) == 0 {
		return platform.ErrUnsupported
	}
	status := ""
	switch state {
	case agent.ApprovalRequestStateResolvedExternally:
		status = approvalStatusResolvedInApp
	case agent.ApprovalRequestStateTurnTerminal:
		status = approvalStatusTurnTerminal
	default:
		return platform.ErrUnsupported
	}
	choice := choices[0]
	action := parsedCardAction{
		Action: cardActionChoice, Kind: cardKindApproval,
		Summary:  approvalSummaryFromPrompt(prompt),
		TaskCard: strings.TrimSpace(choice.Metadata["task_card_id"]),
		Approval: strings.TrimSpace(choice.Metadata["approval_key"]),
		Owner:    strings.TrimSpace(choice.Metadata[approvalOwnerValueKey]),
		Status:   status,
	}
	handled := false
	failures := make([]error, 0, 3)
	if updated, err := r.recordAutomaticApprovalOnTaskCard(ctx, action); updated {
		handled = true
		failures = appendApprovalDisplayError(failures, err)
	}
	if updated, err := r.recordAutomaticApprovalOnPanel(ctx, action); updated {
		handled = true
		failures = appendApprovalDisplayError(failures, err)
	}
	if updated, err := r.recordAutomaticApprovalOnStandaloneCard(ctx, action); updated {
		handled = true
		failures = appendApprovalDisplayError(failures, err)
	}
	if !handled {
		return platform.ErrUnsupported
	}
	return errors.Join(failures...)
}

func appendApprovalDisplayError(failures []error, err error) []error {
	if err != nil {
		return append(failures, err)
	}
	return failures
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
	var updated bool
	var resultErr error
	if err := r.withCardOperation(action.TaskCard, func() error {
		opts, sequence, ok := r.taskCards.addApprovalWithSequence(action.TaskCard, action)
		if !ok {
			return nil
		}
		updated = true
		cardJSON, err := buildCardV2(opts)
		if err != nil {
			resultErr = fmt.Errorf("build automatic approval task card: %w", err)
			return nil
		}
		if err := r.cardKit.UpdateCard(ctx, action.TaskCard, cardJSON, sequence); err != nil {
			resultErr = fmt.Errorf("update automatic approval task card: %w", err)
		}
		return nil
	}); err != nil {
		return false, err
	}
	return updated, resultErr
}

func (r *Replier) recordAutomaticApprovalOnPanel(ctx context.Context, action parsedCardAction) (bool, error) {
	if r.taskCards == nil || strings.TrimSpace(action.TaskCard) == "" {
		return false, nil
	}
	var updated bool
	var resultErr error
	if err := r.withCardOperation(action.TaskCard, func() error {
		snapshot, ok := r.taskCards.completeApprovalPanelItem(action)
		if !ok || strings.TrimSpace(snapshot.CardID) == "" {
			return nil
		}
		updated = true
		cardJSON, err := buildApprovalPanelCardJSON(snapshot)
		if err != nil {
			resultErr = fmt.Errorf("build automatic approval panel card: %w", err)
			return nil
		}
		if err := r.withCardOperation(snapshot.CardID, func() error {
			return r.cardKit.UpdateCard(ctx, snapshot.CardID, cardJSON, snapshot.Seq)
		}); err != nil {
			resultErr = fmt.Errorf("update automatic approval panel card: %w", err)
		}
		return nil
	}); err != nil {
		return false, err
	}
	return updated, resultErr
}

func (r *Replier) recordAutomaticApprovalOnStandaloneCard(ctx context.Context, action parsedCardAction) (bool, error) {
	key := strings.TrimSpace(action.Approval)
	cardID := r.standaloneApprovalCardID(key)
	if cardID == "" {
		return false, nil
	}
	var updated bool
	var resultErr error
	if err := r.withCardOperation(cardID, func() error {
		card, ok := r.nextStandaloneApprovalCard(key)
		if !ok {
			return nil
		}
		updated = true
		cardJSON, err := json.Marshal(buildChoiceHandledCard(action).Data)
		if err != nil {
			resultErr = fmt.Errorf("marshal automatic approval card: %w", err)
			return nil
		}
		if err := r.cardKit.UpdateCard(ctx, card.cardID, string(cardJSON), card.sequence); err != nil {
			resultErr = fmt.Errorf("update automatic approval card: %w", err)
			return nil
		}
		r.forgetStandaloneApprovalCard(key, card.cardID)
		return nil
	}); err != nil {
		return false, err
	}
	return updated, resultErr
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

func (r *Replier) standaloneApprovalCardID(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	r.approvalMu.Lock()
	defer r.approvalMu.Unlock()
	return strings.TrimSpace(r.approvalCard[key].cardID)
}

func (r *Replier) forgetStandaloneApprovalCard(key string, cardID string) {
	r.approvalMu.Lock()
	defer r.approvalMu.Unlock()
	if card, ok := r.approvalCard[key]; ok && card.cardID == cardID {
		delete(r.approvalCard, key)
	}
}
