package agent

import (
	"context"
	"fmt"
	"time"
)

const (
	codexThreadWatchReconcileInterval = 2 * time.Second
	codexThreadWatchRefreshTicks      = 5
)

type codexThreadWatchOptions struct {
	conversationID string
	threadID       string
	targetTurnID   string
	turnCh         <-chan *codexTurnEvent
	onProgress     func(string)
	reconcile      <-chan time.Time
}

// WatchCodexThread 接管已经运行的 Codex thread，并等待当前 turn 完成。
func (a *ACPAgent) WatchCodexThread(ctx context.Context, conversationID string, threadID string, onProgress func(delta string)) (string, error) {
	if a.protocol != protocolCodexAppServer {
		return "", fmt.Errorf("agent is not codex app-server")
	}
	ticker := time.NewTicker(codexThreadWatchReconcileInterval)
	defer ticker.Stop()
	return a.watchCodexThreadWithReconcile(ctx, codexThreadWatchOptions{
		conversationID: conversationID, threadID: threadID,
		onProgress: onProgress, reconcile: ticker.C,
	})
}

// watchCodexThreadWithReconcile 同时消费实时事件和权威状态，避免单个终态事件缺失后永久挂起。
func (a *ACPAgent) watchCodexThreadWithReconcile(ctx context.Context, opts codexThreadWatchOptions) (string, error) {
	if binding, ok := a.desktopBindingForThread(opts.conversationID, opts.threadID); ok {
		if binding.Owner == CodexOwnerDesktopDisconnected {
			return "", ErrCodexDesktopDisconnected
		}
		if binding.Owner == CodexOwnerUnknown {
			return "", ErrCodexDesktopOwnershipUnknown
		}
	}
	turnCh := make(chan *codexTurnEvent, 256)
	if !a.registerTurnChannel(opts.threadID, turnCh) {
		return "", fmt.Errorf("thread %s already has an active watcher or turn", opts.threadID)
	}
	defer a.unregisterTurnChannel(opts.threadID, turnCh)
	state, err := a.ReadCodexThreadState(ctx, opts.conversationID, opts.threadID)
	if err == nil && !state.Active {
		if state.LastAgentMessageText != "" {
			return state.LastAgentMessageText, nil
		}
		return "Codex App 本地任务已完成，但没有返回文本。", nil
	}
	watch := opts
	if err == nil {
		watch.targetTurnID = state.ActiveTurnID
	}
	watch.turnCh = turnCh
	return a.collectAttachedCodexTurn(ctx, watch)
}

func (a *ACPAgent) collectAttachedCodexTurn(ctx context.Context, opts codexThreadWatchOptions) (string, error) {
	assembler := newCodexFinalAssembler()
	diagnostics := newCodexTurnDiagnostics(codexTurnDiagnosticsLimit)
	progressState := newCodexProgressState()
	ticksWithoutEvent := 0
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-opts.reconcile:
			ticksWithoutEvent++
			if ticksWithoutEvent >= codexThreadWatchRefreshTicks {
				if err := a.refreshAttachedCodexThread(ctx, opts.conversationID, opts.threadID); err != nil {
					return "", err
				}
				ticksWithoutEvent = 0
			}
			text, finished, err := a.reconcileAttachedCodexTurn(ctx, opts, assembler)
			if err != nil || finished {
				return text, err
			}
		case evt := <-opts.turnCh:
			if evt.TurnID != "" && opts.targetTurnID != "" && evt.TurnID != opts.targetTurnID {
				continue
			}
			ticksWithoutEvent = 0
			if evt.Approval != nil {
				if err := a.handleAttachedCodexApproval(ctx, evt); err != nil {
					return "", err
				}
				continue
			}
			if evt.UserInput != nil {
				if err := a.handleCodexUserInputEvent(ctx, evt); err != nil {
					return "", fmt.Errorf("user input response error: %w", err)
				}
				continue
			}
			if evt.Kind == "error" {
				return "", fmt.Errorf("%w: %s", ErrCodexTurnTerminal, diagnostics.withError(evt.Text))
			}
			collectCodexTurnText(assembler, evt, opts.onProgress, progressState, diagnostics)
			if evt.Kind == "completed" {
				return a.attachedCodexFinalText(ctx, opts.conversationID, opts.threadID, assembler)
			}
		}
	}
}

// refreshAttachedCodexThread 在 Desktop 事件静默时主动拉取带 revision 屏障的目标状态。
func (a *ACPAgent) refreshAttachedCodexThread(ctx context.Context, conversationID string, threadID string) error {
	binding, ok := a.desktopBindingForThread(conversationID, threadID)
	if !ok || binding.Owner != CodexOwnerDesktopLive || a.desktopRuntime == nil {
		return nil
	}
	return a.desktopRuntime.LoadHistory(ctx, CodexThreadRef{
		ConversationID: conversationID, ThreadID: threadID,
	})
}

// reconcileAttachedCodexTurn 在实时事件缺失时根据当前 active turn 判断原任务是否已经结束。
func (a *ACPAgent) reconcileAttachedCodexTurn(ctx context.Context, opts codexThreadWatchOptions, assembler *codexFinalAssembler) (string, bool, error) {
	state, err := a.ReadCodexThreadState(ctx, opts.conversationID, opts.threadID)
	if err != nil {
		return "", false, err
	}
	if state.Active && (opts.targetTurnID == "" || state.ActiveTurnID == "" || state.ActiveTurnID == opts.targetTurnID) {
		return "", false, nil
	}
	if text := assembler.finalText(); text != "" {
		return text, true, nil
	}
	if state.LastAgentMessageText != "" {
		return state.LastAgentMessageText, true, nil
	}
	return "Codex App 本地任务已完成，但没有返回文本。", true, nil
}

func (a *ACPAgent) handleAttachedCodexApproval(ctx context.Context, evt *codexTurnEvent) error {
	if err := a.handleCodexApprovalEvent(ctx, evt); err != nil {
		return fmt.Errorf("approval response error: %w", err)
	}
	return nil
}

func collectCodexTurnText(assembler *codexFinalAssembler, evt *codexTurnEvent, onProgress func(string), progressState *codexProgressState, diagnostics *codexTurnDiagnostics) {
	if evt.Kind == "progress" {
		if progressText, ok := progressState.record(evt); ok {
			diagnostics.remember(progressText)
			if onProgress != nil {
				onProgress(progressText)
			}
		}
	}
	if evt.Delta != "" {
		if onProgress != nil {
			if progressText, ok := progressState.emitGenerating(); ok {
				diagnostics.remember(progressText)
				onProgress(progressText)
			}
		}
		assembler.addDelta(evt.ItemID, evt.Delta)
	}
	if evt.Text != "" {
		if evt.Kind == "item_completed" {
			assembler.addCompleted(evt.ItemID, evt.Text)
		} else {
			assembler.addSnapshot(evt.ItemID, evt.Text)
		}
	}
}

func (a *ACPAgent) attachedCodexFinalText(ctx context.Context, conversationID string, threadID string, assembler *codexFinalAssembler) (string, error) {
	if text := assembler.finalText(); text != "" {
		return text, nil
	}
	state, err := a.ReadCodexThreadState(ctx, conversationID, threadID)
	if err == nil && state.LastAgentMessageText != "" {
		return state.LastAgentMessageText, nil
	}
	return "Codex App 本地任务已完成，但没有返回文本。", nil
}
