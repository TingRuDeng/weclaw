package agent

import (
	"context"
	"errors"
	"fmt"
)

var (
	errCodexApprovalResponderMissing = errors.New("Codex 审批事件缺少 provider responder")
	errCodexApprovalResponsePending  = errors.New("Codex Desktop 审批仍待处理")
)

// handleCodexApprovalEvent 统一选择决策并调用事件所属 provider 的 responder。
func (a *ACPAgent) handleCodexApprovalEvent(ctx context.Context, evt *codexTurnEvent) error {
	if evt == nil || evt.Approval == nil {
		return nil
	}
	if evt.Approval.Respond == nil {
		return errCodexApprovalResponderMissing
	}
	optionID, resolveErr := a.resolvePermissionOptionWithError(ctx, evt.Approval.Request)
	if resolveErr != nil {
		if errors.Is(resolveErr, ErrApprovalResolvedExternally) || errors.Is(resolveErr, ErrApprovalTurnTerminal) ||
			errors.Is(resolveErr, ErrCodexInteractionResolvedExternally) || errors.Is(resolveErr, ErrCodexTurnTerminal) {
			a.resolveCodexInteraction(evt, resolveErr)
			return nil
		}
		return resolveErr
	}
	if err := a.submitCodexInteraction(ctx, evt, func() error {
		return evt.Approval.Respond(ctx, optionID)
	}); err != nil {
		if errors.Is(err, ErrCodexInteractionResolvedExternally) || errors.Is(err, ErrCodexTurnTerminal) {
			return nil
		}
		if errors.Is(err, ErrCodexDesktopRequestNotFound) {
			recheckErr := recheckCodexDesktopApprovalAfterRequestNotFound(ctx, evt.Approval.Request, err)
			if recheckErr == nil {
				a.resolveCodexInteraction(evt, ErrApprovalResolvedExternally)
			}
			return recheckErr
		}
		return fmt.Errorf("provider approval response: %w", err)
	}
	return nil
}

// recheckCodexDesktopApprovalAfterRequestNotFound 只在权威 history 证明请求已被
// 其他前端处理或原 turn 已结束时幂等成功；仍 pending 或状态不可用时保留交互并重试。
func recheckCodexDesktopApprovalAfterRequestNotFound(ctx context.Context, req ApprovalRequest, responseErr error) error {
	if req.StateProbe == nil {
		return fmt.Errorf("provider approval response: %w", responseErr)
	}
	state, probeErr := req.StateProbe(ctx)
	switch {
	case probeErr != nil:
		return fmt.Errorf("%w: 权威状态复核失败: %v", errCodexApprovalResponsePending, probeErr)
	case state == ApprovalRequestStateResolvedExternally || state == ApprovalRequestStateTurnTerminal:
		return nil
	case state == ApprovalRequestStatePending:
		return fmt.Errorf("%w: request %s", errCodexApprovalResponsePending, req.RequestID)
	default:
		return fmt.Errorf("%w: request %s 状态未知", errCodexApprovalResponsePending, req.RequestID)
	}
}
