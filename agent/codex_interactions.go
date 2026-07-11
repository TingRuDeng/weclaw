package agent

import (
	"context"
	"errors"
	"fmt"
)

var errCodexApprovalResponderMissing = errors.New("Codex 审批事件缺少 provider responder")

// handleCodexApprovalEvent 统一选择决策并调用事件所属 provider 的 responder。
func (a *ACPAgent) handleCodexApprovalEvent(ctx context.Context, evt *codexTurnEvent) error {
	if evt == nil || evt.Approval == nil {
		return nil
	}
	if evt.Approval.Respond == nil {
		return errCodexApprovalResponderMissing
	}
	optionID := a.resolvePermissionOption(ctx, evt.Approval.Request)
	if err := evt.Approval.Respond(ctx, optionID); err != nil {
		return fmt.Errorf("provider approval response: %w", err)
	}
	return nil
}
