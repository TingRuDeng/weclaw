package agent

import (
	"context"
	"fmt"
)

// WatchCodexThread 接管已经运行的 Codex thread，并等待当前 turn 完成。
func (a *ACPAgent) WatchCodexThread(ctx context.Context, conversationID string, threadID string, onProgress func(delta string)) (string, error) {
	if a.protocol != protocolCodexAppServer {
		return "", fmt.Errorf("agent is not codex app-server")
	}
	turnCh := make(chan *codexTurnEvent, 256)
	a.notifyMu.Lock()
	a.turnCh[threadID] = turnCh
	a.notifyMu.Unlock()
	defer func() {
		a.notifyMu.Lock()
		delete(a.turnCh, threadID)
		a.notifyMu.Unlock()
	}()
	if state, err := a.ReadCodexThreadState(ctx, conversationID, threadID); err == nil && !state.Active {
		if state.LastAgentMessageText != "" {
			return state.LastAgentMessageText, nil
		}
		return "Codex App 本地任务已完成，但没有返回文本。", nil
	}
	return a.collectAttachedCodexTurn(ctx, conversationID, threadID, turnCh, onProgress)
}

func (a *ACPAgent) collectAttachedCodexTurn(ctx context.Context, conversationID string, threadID string, turnCh <-chan *codexTurnEvent, onProgress func(string)) (string, error) {
	assembler := newCodexFinalAssembler()
	diagnostics := newCodexTurnDiagnostics(codexTurnDiagnosticsLimit)
	progressState := newCodexProgressState()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case evt := <-turnCh:
			if evt.Approval != nil {
				if err := a.handleAttachedCodexApproval(ctx, evt); err != nil {
					return "", err
				}
				continue
			}
			if evt.Kind == "error" {
				return "", fmt.Errorf("turn error: %s", diagnostics.withError(evt.Text))
			}
			collectCodexTurnText(assembler, evt, onProgress, progressState, diagnostics)
			if evt.Kind == "completed" {
				return a.attachedCodexFinalText(ctx, conversationID, threadID, assembler)
			}
		}
	}
}

func (a *ACPAgent) handleAttachedCodexApproval(ctx context.Context, evt *codexTurnEvent) error {
	optionID := a.resolvePermissionOption(ctx, evt.Approval.Request)
	if err := a.respondPermissionRequest(evt.Approval.ID, optionID, evt.Approval.ResponseFormat, evt.Approval.RequestedPermissions); err != nil {
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
