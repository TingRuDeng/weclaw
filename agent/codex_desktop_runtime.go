package agent

import (
	"context"
	"fmt"
)

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
	_, err := a.desktopRuntime.startTurn(ctx, codexDesktopStartTurnSpec{
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
	return a.collectAttachedCodexTurn(ctx, binding.Ref.ConversationID, threadID, turnCh, onProgress)
}

func (a *ACPAgent) desktopBindingForThread(conversationID string, threadID string) (CodexThreadBinding, bool) {
	binding, ok := a.CurrentCodexThreadBinding(conversationID)
	return binding, ok && binding.Ref.ThreadID == threadID
}
