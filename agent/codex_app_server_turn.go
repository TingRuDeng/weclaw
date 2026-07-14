package agent

import (
	"context"
	"fmt"
	"log"
	"time"
)

type codexTurnMetrics struct {
	startedAt    time.Time
	firstEventAt time.Time
}

func newCodexTurnMetrics(startedAt time.Time) codexTurnMetrics {
	return codexTurnMetrics{startedAt: startedAt}
}

func (m *codexTurnMetrics) markFirstEvent(now time.Time) (time.Duration, bool) {
	if !m.firstEventAt.IsZero() {
		return 0, false
	}
	m.firstEventAt = now
	return now.Sub(m.startedAt), true
}

func (m codexTurnMetrics) elapsed(now time.Time) time.Duration {
	return now.Sub(m.startedAt)
}

func (a *ACPAgent) chatCodexAppServer(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	result, err := a.chatCodexAppServerTurn(ctx, conversationID, message, onProgress)
	if err != nil {
		return "", err
	}
	return result, nil
}

func (a *ACPAgent) chatCodexAppServerTurn(ctx context.Context, conversationID string, message string, onProgress func(delta string)) (string, error) {
	threadID, err := a.requireThread(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("thread error: %w", err)
	}

	pid := 0
	a.mu.Lock()
	if a.cmd != nil && a.cmd.Process != nil {
		pid = a.cmd.Process.Pid
	}
	a.mu.Unlock()

	log.Printf("[acp] reusing thread (pid=%d, thread=%s, conversation=%s)", pid, threadID, conversationID)

	// Register turn event channel
	turnCh := make(chan *codexTurnEvent, 256)
	if !a.registerTurnChannel(threadID, turnCh) {
		return "", fmt.Errorf("thread %s already has an active turn", threadID)
	}
	defer a.unregisterTurnChannel(threadID, turnCh)

	turnMetrics := newCodexTurnMetrics(time.Now())
	turnIDCh := make(chan string, 1)
	activeTurnID := ""

	// Start turn (call returns quickly with turn info, actual content comes via events)
	go func() {
		startTurn := func() error {
			startedAt := time.Now()
			config := a.modelConfigSnapshot()
			result, err := a.rpc(ctx, "turn/start", codexTurnStartParams{
				ThreadID:          threadID,
				ApprovalPolicy:    a.approvalPolicyForContext(ctx),
				ApprovalsReviewer: a.approvalReviewerForCodex(),
				Input:             []codexUserInput{{Type: "text", Text: message}},
				SandboxPolicy:     map[string]interface{}{"type": a.sandboxPolicyTypeForCodex()},
				Model:             config.model,
				Effort:            config.effort,
				Cwd:               a.cwdForConversation(conversationID),
			})
			if turnID := codexTurnIDFromStartResult(result); turnID != "" {
				turnIDCh <- turnID
			}
			elapsed := time.Since(startedAt)
			if err != nil {
				log.Printf("[acp] turn/start failed (pid=%d, thread=%s, conversation=%s, elapsed=%s): %v", pid, threadID, conversationID, elapsed, err)
			} else {
				log.Printf("[acp] turn/start accepted (pid=%d, thread=%s, conversation=%s, elapsed=%s)", pid, threadID, conversationID, elapsed)
			}
			return err
		}

		err := startTurn()
		if err != nil && isMissingThreadError(err) {
			log.Printf("[acp] turn/start failed with missing thread, attempting thread/resume (thread=%s): %v", threadID, err)
			if resumeErr := a.resumeThread(ctx, conversationID, threadID); resumeErr == nil {
				err = startTurn()
			} else {
				err = fmt.Errorf("%w (resume failed: %v)", err, resumeErr)
			}
		}
		if err != nil {
			// If call itself fails, signal via turn channel
			turnCh <- &codexTurnEvent{Kind: "error", Text: err.Error()}
		}
	}()

	// 汇总同一 turn 内的文本事件，避免 snapshot 和 delta 同时出现时重复拼接。
	assembler := newCodexFinalAssembler()
	diagnostics := newCodexTurnDiagnostics(codexTurnDiagnosticsLimit)
	progressState := newCodexProgressState()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[acp] turn context done (pid=%d, thread=%s, conversation=%s, elapsed=%s): %v", pid, threadID, conversationID, turnMetrics.elapsed(time.Now()), ctx.Err())
			if err := a.interruptCancelledCodexTurn(threadID, activeTurnID, turnIDCh); err != nil {
				return "", fmt.Errorf("%w: remote turn interrupt failed: %v", ctx.Err(), err)
			}
			return "", ctx.Err()
		case activeTurnID = <-turnIDCh:
			continue
		case evt := <-turnCh:
			if latency, ok := turnMetrics.markFirstEvent(time.Now()); ok {
				log.Printf("[acp] first turn event (pid=%d, thread=%s, conversation=%s, kind=%s, elapsed=%s)", pid, threadID, conversationID, evt.Kind, latency)
			}
			if evt.Approval != nil {
				diagnostics.remember("进展：Codex 请求权限审批。")
				if onProgress != nil {
					onProgress("进展：Codex 请求权限审批。")
				}
				progressState.emitted = true
				if err := a.handleCodexApprovalEvent(ctx, evt); err != nil {
					log.Printf("[acp] turn approval response failed (pid=%d, thread=%s, conversation=%s, elapsed=%s): %v", pid, threadID, conversationID, turnMetrics.elapsed(time.Now()), err)
					return "", fmt.Errorf("approval response error: %w", err)
				}
				continue
			}
			if evt.UserInput != nil {
				diagnostics.remember("进展：Codex 请求补充信息。")
				if onProgress != nil {
					onProgress("进展：Codex 请求补充信息。")
				}
				progressState.emitted = true
				if err := a.handleCodexUserInputEvent(ctx, evt); err != nil {
					return "", fmt.Errorf("user input response error: %w", err)
				}
				continue
			}
			if evt.Kind == "interrupted" {
				turnID := firstNonEmpty(evt.TurnID, activeTurnID)
				log.Printf("[acp] turn observation interrupted (pid=%d, thread=%s, turn=%s, conversation=%s, elapsed=%s)", pid, threadID, turnID, conversationID, turnMetrics.elapsed(time.Now()))
				return "", &CodexTurnInterruptedError{ThreadID: threadID, TurnID: turnID}
			}
			if evt.Kind == "error" {
				errorText := diagnostics.withError(evt.Text)
				log.Printf("[acp] turn failed (pid=%d, thread=%s, conversation=%s, elapsed=%s): %.200s", pid, threadID, conversationID, turnMetrics.elapsed(time.Now()), errorText)
				return "", fmt.Errorf("turn error: %s", errorText)
			}
			if evt.Kind == "progress" {
				if progressText, ok := progressState.record(evt); ok {
					diagnostics.remember(progressText)
					if onProgress != nil {
						onProgress(progressText)
					}
				}
				continue
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
			if evt.Kind == "completed" {
				log.Printf("[acp] turn completed (pid=%d, thread=%s, conversation=%s, elapsed=%s)", pid, threadID, conversationID, turnMetrics.elapsed(time.Now()))
				result := assembler.finalText()
				if result == "" {
					return "", fmt.Errorf("agent returned empty response")
				}
				return result, nil
			}
		}
	}
}

// clearCodexThread 清理指定 conversation 的 thread 映射，仅供用户显式切换或新建会话。
func (a *ACPAgent) clearCodexThread(conversationID string) string {
	a.mu.Lock()
	oldThreadID := a.threads[conversationID]
	delete(a.threads, conversationID)
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	if a.codexOwners != nil {
		a.codexOwners.unbindConversation(conversationID)
	}
	a.persistState()
	return oldThreadID
}
