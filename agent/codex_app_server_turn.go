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

const codexTurnEventBufferSize = 256

type codexAppServerTurnOptions struct {
	ctx            context.Context
	conversationID string
	message        string
	onProgress     func(string)
	onStarted      func(string) error
}

type codexAppServerTurnRuntime struct {
	opts         codexAppServerTurnOptions
	threadID     string
	pid          int
	turnCh       chan *codexTurnEvent
	turnIDCh     chan string
	activeTurnID string
	metrics      codexTurnMetrics
	assembler    *codexFinalAssembler
	diagnostics  *codexTurnDiagnostics
	progress     *codexProgressState
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

func (a *ACPAgent) chatCodexAppServer(opts codexAppServerTurnOptions) (string, error) {
	return a.chatCodexAppServerControlledTurn(opts)
}

func (a *ACPAgent) chatCodexAppServerControlledTurn(opts codexAppServerTurnOptions) (string, error) {
	permit, err := a.ensureCodexAppServerGate().acquire(opts.ctx)
	if err != nil {
		return "", err
	}
	defer permit.release()
	threadID, err := a.requireThread(opts.ctx, opts.conversationID)
	if err != nil {
		return "", fmt.Errorf("thread error: %w", err)
	}
	runtime := &codexAppServerTurnRuntime{
		opts: opts, threadID: threadID, pid: a.runtimePID(),
		turnCh: make(chan *codexTurnEvent, codexTurnEventBufferSize), turnIDCh: make(chan string, 1),
		metrics: newCodexTurnMetrics(time.Now()), assembler: newCodexFinalAssembler(),
		diagnostics: newCodexTurnDiagnostics(codexTurnDiagnosticsLimit), progress: newCodexProgressState(),
	}
	if !a.registerTurnChannel(threadID, runtime.turnCh) {
		return "", fmt.Errorf("thread %s already has an active turn", threadID)
	}
	defer a.unregisterTurnChannel(threadID, runtime.turnCh)
	log.Printf("[acp] reusing thread (pid=%d, thread=%s, conversation=%s)", runtime.pid, threadID, opts.conversationID)
	a.startCodexAppServerTurn(runtime)
	return a.collectCodexAppServerTurn(runtime)
}

func (a *ACPAgent) startCodexAppServerTurn(runtime *codexAppServerTurnRuntime) {
	go func() {
		err := a.callCodexAppServerTurnStart(runtime)
		if err != nil && isMissingThreadError(err) {
			log.Printf("[acp] turn/start failed with missing thread, attempting thread/resume (thread=%s): %v", runtime.threadID, err)
			if resumeErr := a.resumeThread(runtime.opts.ctx, runtime.opts.conversationID, runtime.threadID); resumeErr == nil {
				err = a.callCodexAppServerTurnStart(runtime)
			} else {
				err = fmt.Errorf("%w (resume failed: %v)", err, resumeErr)
			}
		}
		if err != nil {
			runtime.turnCh <- &codexTurnEvent{Kind: "error", Text: err.Error()}
		}
	}()
}

func (a *ACPAgent) callCodexAppServerTurnStart(runtime *codexAppServerTurnRuntime) error {
	startedAt := time.Now()
	config := a.modelConfigSnapshot()
	result, err := a.rpc(runtime.opts.ctx, "turn/start", codexTurnStartParams{
		ThreadID: runtime.threadID, ApprovalPolicy: a.approvalPolicyForContext(runtime.opts.ctx),
		ApprovalsReviewer: a.approvalReviewerForCodex(),
		Input:             []codexUserInput{{Type: "text", Text: runtime.opts.message}},
		SandboxPolicy:     map[string]interface{}{"type": a.sandboxPolicyTypeForCodex()},
		Model:             config.model, Effort: config.effort, Cwd: a.cwdForConversation(runtime.opts.conversationID),
	})
	if turnID := codexTurnIDFromStartResult(result); turnID != "" {
		if runtime.opts.onStarted != nil {
			if acceptErr := runtime.opts.onStarted(turnID); acceptErr != nil {
				return a.rejectStartedCodexTurn(runtime.threadID, turnID, acceptErr)
			}
		}
		runtime.turnIDCh <- turnID
	} else if runtime.opts.onStarted != nil && err == nil {
		err = fmt.Errorf("Codex turn/start 响应缺少 turn ID")
	}
	a.logCodexTurnStart(runtime, time.Since(startedAt), err)
	return err
}

func (a *ACPAgent) logCodexTurnStart(runtime *codexAppServerTurnRuntime, elapsed time.Duration, err error) {
	if err != nil {
		log.Printf("[acp] turn/start failed (pid=%d, thread=%s, conversation=%s, elapsed=%s): %v", runtime.pid, runtime.threadID, runtime.opts.conversationID, elapsed, err)
		return
	}
	log.Printf("[acp] turn/start accepted (pid=%d, thread=%s, conversation=%s, elapsed=%s)", runtime.pid, runtime.threadID, runtime.opts.conversationID, elapsed)
}

func (a *ACPAgent) collectCodexAppServerTurn(runtime *codexAppServerTurnRuntime) (string, error) {
	for {
		select {
		case <-runtime.opts.ctx.Done():
			return a.cancelCodexAppServerTurn(runtime)
		case runtime.activeTurnID = <-runtime.turnIDCh:
		case evt := <-runtime.turnCh:
			result, done, err := a.handleCodexAppServerEvent(runtime, evt)
			if done || err != nil {
				return result, err
			}
		}
	}
}

func (a *ACPAgent) cancelCodexAppServerTurn(runtime *codexAppServerTurnRuntime) (string, error) {
	err := runtime.opts.ctx.Err()
	log.Printf("[acp] turn context done (pid=%d, thread=%s, conversation=%s, elapsed=%s): %v", runtime.pid, runtime.threadID, runtime.opts.conversationID, runtime.metrics.elapsed(time.Now()), err)
	if interruptErr := a.interruptCancelledCodexTurn(runtime.threadID, runtime.activeTurnID, runtime.turnIDCh); interruptErr != nil {
		return "", fmt.Errorf("%w: remote turn interrupt failed: %v", err, interruptErr)
	}
	return "", err
}

func (a *ACPAgent) handleCodexAppServerEvent(runtime *codexAppServerTurnRuntime, evt *codexTurnEvent) (string, bool, error) {
	if latency, ok := runtime.metrics.markFirstEvent(time.Now()); ok {
		log.Printf("[acp] first turn event (pid=%d, thread=%s, conversation=%s, kind=%s, elapsed=%s)", runtime.pid, runtime.threadID, runtime.opts.conversationID, evt.Kind, latency)
	}
	if handled, err := a.handleCodexAppServerInteraction(runtime, evt); handled {
		return "", false, err
	}
	if result, done, err := handleCodexAppServerTerminal(runtime, evt); done {
		return result, true, err
	}
	if evt.Kind == "progress" {
		emitCodexAppServerProgress(runtime, evt)
		return "", false, nil
	}
	collectCodexAppServerContent(runtime, evt)
	if evt.Kind != "completed" {
		return "", false, nil
	}
	return finishCodexAppServerTurn(runtime)
}

func (a *ACPAgent) handleCodexAppServerInteraction(runtime *codexAppServerTurnRuntime, evt *codexTurnEvent) (bool, error) {
	progressText := ""
	var handle func() error
	if evt.Approval != nil {
		progressText = "进展：Codex 请求权限审批。"
		handle = func() error { return a.handleCodexApprovalEvent(runtime.opts.ctx, evt) }
	} else if evt.UserInput != nil {
		progressText = "进展：Codex 请求补充信息。"
		handle = func() error { return a.handleCodexUserInputEvent(runtime.opts.ctx, evt) }
	} else {
		return false, nil
	}
	runtime.diagnostics.remember(progressText)
	if runtime.opts.onProgress != nil {
		runtime.opts.onProgress(progressText)
	}
	runtime.progress.emitted = true
	if err := handle(); err != nil {
		return true, fmt.Errorf("Codex 交互响应失败: %w", err)
	}
	return true, nil
}

func handleCodexAppServerTerminal(runtime *codexAppServerTurnRuntime, evt *codexTurnEvent) (string, bool, error) {
	if evt.Kind == "interrupted" {
		turnID := firstNonEmpty(evt.TurnID, runtime.activeTurnID)
		log.Printf("[acp] turn observation interrupted (pid=%d, thread=%s, turn=%s, conversation=%s, elapsed=%s)", runtime.pid, runtime.threadID, turnID, runtime.opts.conversationID, runtime.metrics.elapsed(time.Now()))
		return "", true, &CodexTurnInterruptedError{ThreadID: runtime.threadID, TurnID: turnID}
	}
	if evt.Kind != "error" {
		return "", false, nil
	}
	errorText := runtime.diagnostics.withError(evt.Text)
	log.Printf("[acp] turn failed (pid=%d, thread=%s, conversation=%s, elapsed=%s): %.200s", runtime.pid, runtime.threadID, runtime.opts.conversationID, runtime.metrics.elapsed(time.Now()), errorText)
	return "", true, fmt.Errorf("turn error: %s", errorText)
}

func emitCodexAppServerProgress(runtime *codexAppServerTurnRuntime, evt *codexTurnEvent) {
	progressText, ok := runtime.progress.record(evt)
	if !ok {
		return
	}
	runtime.diagnostics.remember(progressText)
	if runtime.opts.onProgress != nil {
		runtime.opts.onProgress(progressText)
	}
}

func collectCodexAppServerContent(runtime *codexAppServerTurnRuntime, evt *codexTurnEvent) {
	if evt.Delta != "" {
		if runtime.opts.onProgress != nil {
			if progressText, ok := runtime.progress.emitGenerating(); ok {
				runtime.diagnostics.remember(progressText)
				runtime.opts.onProgress(progressText)
			}
		}
		runtime.assembler.addDelta(evt.ItemID, evt.Delta)
	}
	if evt.Text == "" {
		return
	}
	if evt.Kind == "item_completed" {
		runtime.assembler.addCompleted(evt.ItemID, evt.Text)
		return
	}
	runtime.assembler.addSnapshot(evt.ItemID, evt.Text)
}

func finishCodexAppServerTurn(runtime *codexAppServerTurnRuntime) (string, bool, error) {
	log.Printf("[acp] turn completed (pid=%d, thread=%s, conversation=%s, elapsed=%s)", runtime.pid, runtime.threadID, runtime.opts.conversationID, runtime.metrics.elapsed(time.Now()))
	result := runtime.assembler.finalText()
	if result == "" {
		return "", true, fmt.Errorf("agent returned empty response")
	}
	return result, true, nil
}

func (a *ACPAgent) rejectStartedCodexTurn(threadID string, turnID string, cause error) error {
	interruptCtx, cancel := context.WithTimeout(context.Background(), codexInterruptTimeout)
	defer cancel()
	_, err := a.rpc(interruptCtx, "turn/interrupt", map[string]interface{}{
		"threadId": threadID, "turnId": turnID,
	})
	if err != nil {
		return fmt.Errorf("%w；中断已启动 turn 失败: %v", cause, err)
	}
	return cause
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
