package feishu

import (
	"context"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

func (r *Replier) refreshApprovalWaiting(ctx context.Context, cardID string) {
	if r.taskCards == nil || r.cardKit == nil {
		return
	}
	if err := r.taskCards.withApprovalCardOperation(cardID, func(target string) error {
		opts, ok := r.taskCards.snapshot(target)
		if !ok || opts.WaitingApprovals == 0 || (opts.Status != cardStatusThinking && opts.Status != cardStatusStreaming) {
			return nil
		}
		card, err := buildCardV2(opts)
		if err != nil {
			return err
		}
		return r.cardKit.UpdateCard(ctx, target, card, r.taskCards.nextSequence(target, 0))
	}); err != nil {
		log.Printf("[feishu] failed to refresh private approval wait: %v", err)
	}
}

func (r *Replier) recordPrivateApprovalWaiting(ctx context.Context, choices []platform.Choice, target string) {
	if r.taskCards == nil || target == r.openID || len(choices) == 0 {
		return
	}
	choice := choices[0]
	if !strings.Contains(choice.Metadata[feishuSessionMetadataKey], ":group:") || choice.Metadata["approval_key"] == "" {
		return
	}
	err := r.taskCards.withApprovalCardOperation(r.CurrentTaskCardID(), func(cardID string) error {
		opts, sequence, ok := r.taskCards.setApprovalWaiting(cardID, choice.Metadata["approval_key"])
		if !ok {
			return nil
		}
		card, err := buildCardV2(opts)
		if err != nil {
			return err
		}
		return r.cardKit.UpdateCard(ctx, cardID, card, sequence)
	})
	if err != nil {
		log.Printf("[feishu] failed to display private approval wait: %v", err)
	}
}

func (r *taskCardRegistry) setApprovalWaiting(cardID, key string) (cardOptions, int, bool) {
	if r == nil || key == "" {
		return cardOptions{}, 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.cards[cardID]
	if s == nil || s.resolvedApprovals[key] || (s.status != cardStatusThinking && s.status != cardStatusStreaming) {
		return cardOptions{}, 0, false
	}
	if s.waitingApprovals == nil {
		s.waitingApprovals = make(map[string]bool)
	}
	s.waitingApprovals[key] = true
	s.sequence++
	s.updatedAt = r.nowOrDefault()
	return s.cardOptions(), s.sequence, true
}

func (r *taskCardRegistry) moveApprovalWaiting(from, to string) {
	if r == nil || from == "" || from == to {
		return
	}
	_ = r.withCardOperation(from, func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		old, next := r.cards[from], r.cards[to]
		if old == nil || next == nil || next.approvalSuccessor != "" || (old.status != cardStatusThinking && old.status != cardStatusStreaming && old.status != cardStatusSuperseded) {
			return nil
		}
		old.approvalSuccessor = to
		if next.waitingApprovals == nil {
			next.waitingApprovals = make(map[string]bool)
		}
		for key := range old.waitingApprovals {
			next.waitingApprovals[key] = true
		}
		if next.resolvedApprovals == nil {
			next.resolvedApprovals = make(map[string]bool)
		}
		for key := range old.resolvedApprovals {
			next.resolvedApprovals[key] = true
			delete(next.waitingApprovals, key)
		}
		old.waitingApprovals = nil
		return nil
	})
}

func (r *taskCardRegistry) approvalCardID(cardID string) string {
	if r == nil {
		return cardID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		s := r.cards[cardID]
		if s == nil || s.approvalSuccessor == "" {
			return cardID
		}
		cardID = s.approvalSuccessor
	}
}

// Resolve again under the card operation lock if a reanchor raced with completion.
func (r *taskCardRegistry) withApprovalCardOperation(cardID string, fn func(string) error) error {
	for {
		target := r.approvalCardID(cardID)
		retry := false
		err := r.withCardOperation(target, func() error {
			if r.approvalCardID(cardID) != target {
				retry = true
				return nil
			}
			return fn(target)
		})
		if !retry {
			return err
		}
	}
}
