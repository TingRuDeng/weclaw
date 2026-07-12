package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// chatCodexDesktopWithRecovery 仅在 Desktop 明确无人处理时恢复同一 thread 并重试一次。
func (a *ACPAgent) chatCodexDesktopWithRecovery(ctx context.Context, binding CodexThreadBinding, message string, onProgress func(string)) (string, error) {
	reply, err := a.chatCodexDesktop(ctx, binding, message, onProgress)
	if !errors.Is(err, ErrCodexDesktopNoClient) {
		return reply, err
	}
	if _, released := a.codexOwners.confirmDesktopReleased(binding.Ref.ThreadID); !released {
		return "", err
	}
	a.persistState()
	if recoverErr := a.RecoverCodexThread(ctx, binding.Ref); recoverErr != nil {
		return "", fmt.Errorf("Codex Desktop 已释放，恢复 thread 失败: %w", recoverErr)
	}
	return a.chatCodexAppServer(ctx, binding.Ref.ConversationID, message, onProgress)
}

// chatCodexDesktop 先订阅事件，再在同一 Desktop thread 开始 turn。
func (a *ACPAgent) chatCodexDesktop(ctx context.Context, binding CodexThreadBinding, message string, onProgress func(string)) (string, error) {
	if a.desktopRuntime == nil {
		return "", ErrCodexDesktopUnavailable
	}
	threadID := binding.Ref.ThreadID
	turnCh := make(chan *codexTurnEvent, 256)
	if !a.registerTurnChannel(threadID, turnCh) {
		return "", fmt.Errorf("thread %s already has an active turn", threadID)
	}
	defer a.unregisterTurnChannel(threadID, turnCh)
	config := a.modelConfigSnapshot()
	turnID, err := a.desktopRuntime.startTurn(ctx, codexDesktopStartTurnSpec{
		ConversationID: threadID, Input: []codexUserInput{{Type: "text", Text: message}},
		Cwd:               a.cwdForConversation(binding.Ref.ConversationID),
		ApprovalPolicy:    a.approvalPolicyForContext(ctx),
		ApprovalsReviewer: a.approvalReviewerForCodex(),
		SandboxPolicy:     map[string]any{"type": a.sandboxPolicyTypeForCodex()},
		Model:             config.model, Effort: config.effort,
	})
	if err != nil {
		return "", err
	}
	ticker := time.NewTicker(codexThreadWatchReconcileInterval)
	defer ticker.Stop()
	return a.collectAttachedCodexTurn(ctx, codexThreadWatchOptions{
		conversationID: binding.Ref.ConversationID, threadID: threadID,
		targetTurnID: turnID, turnCh: turnCh, onProgress: onProgress, reconcile: ticker.C,
	})
}

func (a *ACPAgent) desktopBindingForThread(conversationID string, threadID string) (CodexThreadBinding, bool) {
	binding, ok := a.CurrentCodexThreadBinding(conversationID)
	return binding, ok && binding.Ref.ThreadID == threadID
}
