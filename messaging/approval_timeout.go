package messaging

import (
	"context"
	"errors"
	"log"

	"github.com/fastclaw-ai/weclaw/platform"
)

// Local timeout is a display outcome, not an upstream approval state.
type approvalTimeoutRecorder interface {
	RecordApprovalTimeout(context.Context, string, []platform.Choice) error
}

func (h *Handler) recordApprovalTimeoutAsync(ctx context.Context, opts agentInteractionContextOptions, pending *pendingApproval) {
	recorder, ok := optionalApprovalTimeoutRecorder(opts.reply)
	if !ok {
		return
	}
	go func() {
		displayCtx, cancel := context.WithTimeout(context.WithoutCancel(normalizeContext(ctx)), automaticApprovalDisplayTimeout)
		defer cancel()
		if err := recorder.RecordApprovalTimeout(displayCtx, pending.automaticPrompt, pending.stateChoices); err != nil && !errors.Is(err, platform.ErrUnsupported) {
			log.Printf("[handler] approval timeout display update failed for %s: %v", opts.actorUserID, err)
			h.auditRecord(auditEntry{User: opts.actorUserID, Action: "approval_timeout_display_failed", Summary: "reason=card_update_failed"})
		}
	}()
}

func optionalApprovalTimeoutRecorder(reply platform.Replier) (approvalTimeoutRecorder, bool) {
	if serialized, ok := reply.(*serializedReplier); ok {
		recorder, supported := optionalApprovalTimeoutRecorder(serialized.inner)
		if !supported {
			return nil, false
		}
		return serializedApprovalTimeoutRecorder{serialized, recorder}, true
	}
	recorder, ok := reply.(approvalTimeoutRecorder)
	return recorder, ok
}

type serializedApprovalTimeoutRecorder struct {
	reply    *serializedReplier
	recorder approvalTimeoutRecorder
}

func (s serializedApprovalTimeoutRecorder) RecordApprovalTimeout(ctx context.Context, prompt string, choices []platform.Choice) error {
	s.reply.mu.Lock()
	defer s.reply.mu.Unlock()
	return s.recorder.RecordApprovalTimeout(ctx, prompt, choices)
}
