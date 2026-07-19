package agent

import (
	"context"
	"fmt"
	"time"
)

type codexDesktopTurnOptions struct {
	ctx        context.Context
	binding    CodexThreadBinding
	message    string
	onProgress func(string)
	onStarted  func(string) error
}

func (a *ACPAgent) chatCodexDesktopTurn(opts codexDesktopTurnOptions) (string, error) {
	if a.desktopRuntime == nil {
		return "", ErrCodexDesktopUnavailable
	}
	threadID := opts.binding.Ref.ThreadID
	turnCh := make(chan *codexTurnEvent, codexTurnEventBufferSize)
	if !a.registerTurnChannel(threadID, turnCh) {
		return "", fmt.Errorf("thread %s already has an active turn", threadID)
	}
	defer a.unregisterTurnChannel(threadID, turnCh)
	turnID, err := a.desktopRuntime.startTurn(opts.ctx, codexDesktopStartTurnSpec{
		ConversationID: threadID, Input: []codexUserInput{{Type: "text", Text: opts.message}},
		Cwd:               a.cwdForConversation(opts.binding.Ref.ConversationID),
		ApprovalPolicy:    a.approvalPolicyForContext(opts.ctx),
		ApprovalsReviewer: a.approvalReviewerForCodex(),
		SandboxPolicy:     map[string]any{"type": a.sandboxPolicyTypeForCodex()},
	})
	if err != nil {
		return "", err
	}
	if opts.onStarted != nil {
		if err := opts.onStarted(turnID); err != nil {
			interruptCtx, cancel := context.WithTimeout(context.Background(), codexInterruptTimeout)
			defer cancel()
			if interruptErr := a.desktopRuntime.interruptTurn(interruptCtx, threadID, turnID); interruptErr != nil {
				return "", fmt.Errorf("%w；中断已启动 Desktop turn 失败: %v", err, interruptErr)
			}
			return "", err
		}
	}
	ticker := time.NewTicker(codexThreadWatchReconcileInterval)
	defer ticker.Stop()
	return a.collectAttachedCodexTurn(opts.ctx, codexThreadWatchOptions{
		conversationID: opts.binding.Ref.ConversationID, threadID: threadID,
		targetTurnID: turnID, turnCh: turnCh, onProgress: opts.onProgress, reconcile: ticker.C,
	})
}

// runtimeBindingForThread 返回 conversation 当前选中 thread 的进程内运行时快照。
func (a *ACPAgent) runtimeBindingForThread(conversationID string, threadID string) (CodexThreadBinding, bool) {
	if a.codexOwners == nil {
		return CodexThreadBinding{}, false
	}
	binding, ok := a.codexOwners.currentConversationBinding(conversationID)
	return binding, ok && binding.Ref.ThreadID == threadID
}
