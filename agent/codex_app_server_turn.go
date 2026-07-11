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
	result, err := a.chatCodexAppServerWithRetry(ctx, conversationID, message, true, onProgress)
	if err != nil {
		return "", err
	}
	a.recordConversationExchange(conversationID, message, result)
	return result, nil
}

func (a *ACPAgent) chatCodexAppServerWithRetry(ctx context.Context, conversationID string, message string, allowFreshRetry bool, onProgress func(delta string)) (string, error) {
	if err := a.refreshCodexRuntimeAfterUsageLimit(ctx, conversationID); err != nil {
		return "", err
	}
	threadID, isNew, err := a.getOrCreateThread(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("thread error: %w", err)
	}

	pid := 0
	a.mu.Lock()
	if a.cmd != nil && a.cmd.Process != nil {
		pid = a.cmd.Process.Pid
	}
	a.mu.Unlock()

	if isNew {
		log.Printf("[acp] new thread created (pid=%d, thread=%s, conversation=%s)", pid, threadID, conversationID)
	} else {
		log.Printf("[acp] reusing thread (pid=%d, thread=%s, conversation=%s)", pid, threadID, conversationID)
	}

	// Register turn event channel
	turnCh := make(chan *codexTurnEvent, 256)
	a.notifyMu.Lock()
	a.turnCh[threadID] = turnCh
	a.notifyMu.Unlock()

	defer func() {
		a.notifyMu.Lock()
		delete(a.turnCh, threadID)
		a.notifyMu.Unlock()
	}()

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
				optionID := a.resolvePermissionOption(ctx, evt.Approval.Request)
				if err := a.respondPermissionRequest(evt.Approval.ID, optionID, evt.Approval.ResponseFormat, evt.Approval.RequestedPermissions); err != nil {
					log.Printf("[acp] turn approval response failed (pid=%d, thread=%s, conversation=%s, elapsed=%s): %v", pid, threadID, conversationID, turnMetrics.elapsed(time.Now()), err)
					return "", fmt.Errorf("approval response error: %w", err)
				}
				continue
			}
			if evt.Kind == "error" {
				errorText := diagnostics.withError(evt.Text)
				log.Printf("[acp] turn failed (pid=%d, thread=%s, conversation=%s, elapsed=%s): %.200s", pid, threadID, conversationID, turnMetrics.elapsed(time.Now()), errorText)
				if allowFreshRetry && !isNew && isMissingThreadError(fmt.Errorf("%s", errorText)) {
					log.Printf("[acp] stale thread error detected, retrying with a fresh thread (conversation=%s, oldThread=%s)", conversationID, threadID)
					return a.retryWithFreshThread(ctx, conversationID, message, "stale_thread_error", onProgress)
				}
				if isCodexAuthStateError(errorText) {
					a.invalidateCodexRuntime(conversationID, "auth_state_error")
					return "", fmt.Errorf("turn error: %s；已刷新 Codex 进程，请重试当前消息", errorText)
				}
				if isCodexUsageLimitError(errorText) {
					a.markCodexUsageLimitRefresh(conversationID)
					return "", fmt.Errorf("turn error: %s；如果你已经手动切换 Codex 账号，下一次请求会刷新 Codex 进程并创建新会话", errorText)
				}
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
					if allowFreshRetry && !isNew {
						log.Printf("[acp] empty response on reused thread, retrying with a fresh thread (conversation=%s, oldThread=%s)", conversationID, threadID)
						return a.retryWithFreshThread(ctx, conversationID, message, "empty_response", onProgress)
					}
					return "", fmt.Errorf("agent returned empty response")
				}
				return result, nil
			}
		}
	}
}

// refreshCodexRuntimeAfterUsageLimit 在额度错误后的下一次请求前切换到当前本机 Codex 登录态。
func (a *ACPAgent) refreshCodexRuntimeAfterUsageLimit(ctx context.Context, conversationID string) error {
	if !a.takeCodexUsageLimitRefresh(conversationID) {
		return nil
	}
	oldThreadID := a.clearCodexThread(conversationID)
	log.Printf("[acp] refreshing codex runtime after usage limit (conversation=%s, oldThread=%s)", conversationID, oldThreadID)
	if a.rpcCall != nil {
		return nil
	}
	a.Stop()
	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("refresh codex runtime after usage limit: %w", err)
	}
	return nil
}

// markCodexUsageLimitRefresh 标记下一次请求需要刷新 runtime，等待用户手动切换账号。
func (a *ACPAgent) markCodexUsageLimitRefresh(conversationID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usageLimitRefreshOnNextTurn[conversationID] = true
}

// takeCodexUsageLimitRefresh 取出并清除额度错误后的刷新标记。
func (a *ACPAgent) takeCodexUsageLimitRefresh(conversationID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	shouldRefresh := a.usageLimitRefreshOnNextTurn[conversationID]
	delete(a.usageLimitRefreshOnNextTurn, conversationID)
	return shouldRefresh
}

func (a *ACPAgent) retryWithFreshThread(ctx context.Context, conversationID string, message string, reason string, onProgress func(delta string)) (string, error) {
	oldThreadID := a.clearCodexThread(conversationID)

	log.Printf("[acp] cleared stale thread mapping (conversation=%s, oldThread=%s, reason=%s), creating fresh thread", conversationID, oldThreadID, reason)
	retryMessage := message
	if hydrated, ok := a.buildRehydratePrompt(conversationID, message); ok {
		retryMessage = hydrated
		log.Printf("[acp] using local context rehydrate prompt for fresh thread (conversation=%s)", conversationID)
	}

	result, err := a.chatCodexAppServerWithRetry(ctx, conversationID, retryMessage, false, onProgress)
	if err != nil {
		return "", fmt.Errorf("retry with fresh thread failed: %w", err)
	}
	return result, nil
}

// invalidateCodexRuntime 在账号态异常时丢弃旧进程，避免后续请求继续使用失效登录态。
func (a *ACPAgent) invalidateCodexRuntime(conversationID string, reason string) {
	oldThreadID := a.clearCodexThread(conversationID)
	log.Printf("[acp] invalidating codex runtime (conversation=%s, oldThread=%s, reason=%s)", conversationID, oldThreadID, reason)
	a.Stop()
}

// clearCodexThread 只清理远端 thread 映射，保留本地历史用于后续恢复上下文。
func (a *ACPAgent) clearCodexThread(conversationID string) string {
	a.mu.Lock()
	oldThreadID := a.threads[conversationID]
	delete(a.threads, conversationID)
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	a.persistState()
	return oldThreadID
}
