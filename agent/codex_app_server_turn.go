package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

type codexTurnMetrics struct {
	startedAt    time.Time
	firstEventAt time.Time
}

const codexTurnEventBufferSize = 256

type codexAppServerTurnOptions struct {
	ctx             context.Context
	conversationID  string
	message         string
	onProgress      func(string)
	onProgressEvent func(ProgressEvent)
	onStarted       func(string) error
	permit          *codexAppServerPermit
}

type codexAppServerTurnRuntime struct {
	opts            codexAppServerTurnOptions
	threadID        string
	pid             int
	turnCh          chan *codexTurnEvent
	turnIDCh        chan string
	startResultCh   chan error
	activeTurnID    string
	metrics         codexTurnMetrics
	assembler       *codexFinalAssembler
	diagnostics     *codexTurnDiagnostics
	messageProgress codexMessageProgressBuffer
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
	permit := opts.permit
	if permit == nil {
		var err error
		permit, err = a.ensureCodexAppServerGate().acquire(opts.ctx)
		if err != nil {
			return "", err
		}
		defer permit.release()
	}
	if err := a.ensureCodexAppServerStartedForTurn(opts.ctx, opts.conversationID); err != nil {
		return "", err
	}
	if err := a.validateCodexAccountForWrite(opts.ctx); err != nil {
		return "", err
	}
	threadID, err := a.requireThread(opts.ctx, opts.conversationID)
	if err != nil {
		return "", fmt.Errorf("thread error: %w", err)
	}
	runtime := &codexAppServerTurnRuntime{
		opts: opts, threadID: threadID, pid: a.runtimePID(),
		turnCh: make(chan *codexTurnEvent, codexTurnEventBufferSize), turnIDCh: make(chan string, 1),
		startResultCh: make(chan error, 1),
		metrics:       newCodexTurnMetrics(time.Now()), assembler: newCodexFinalAssembler(),
		diagnostics: newCodexTurnDiagnostics(codexTurnDiagnosticsLimit),
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
		runtime.startResultCh <- err
	}()
}

func (a *ACPAgent) callCodexAppServerTurnStart(runtime *codexAppServerTurnRuntime) error {
	startedAt := time.Now()
	result, err := a.rpc(runtime.opts.ctx, "turn/start", codexTurnStartParams{
		ThreadID: runtime.threadID, ApprovalPolicy: a.approvalPolicyForContext(runtime.opts.ctx),
		ApprovalsReviewer: a.approvalReviewerForCodex(),
		Input:             []codexUserInput{{Type: "text", Text: runtime.opts.message}},
		SandboxPolicy:     map[string]interface{}{"type": a.sandboxPolicyTypeForCodex()},
		Cwd:               a.cwdForConversation(runtime.opts.conversationID),
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
	detach := codexObserverDetachFromContext(runtime.opts.ctx)
	startResultCh := runtime.startResultCh
	for {
		select {
		case <-detach:
			return detachCodexAppServerTurn(runtime)
		case <-runtime.opts.ctx.Done():
			return a.cancelCodexAppServerTurn(runtime)
		case err := <-startResultCh:
			startResultCh = nil
			if err != nil {
				return a.handleCodexAppServerTurnStartError(runtime, err)
			}
		case runtime.activeTurnID = <-runtime.turnIDCh:
		case evt := <-runtime.turnCh:
			result, done, err := a.handleCodexAppServerEvent(runtime, evt)
			if !done && err == nil {
				continue
			}
			// app-server 可以在 turn/start 响应到达前投递进度、审批甚至终态。
			// 非终态必须立即处理，避免服务端等待审批响应；但本地生命周期
			// 只有在 OnTurnStarted 已提交后才能结束，否则迟到的 accept 会失败。
			if startResultCh != nil {
				select {
				case <-detach:
					return detachCodexAppServerTurn(runtime)
				case <-runtime.opts.ctx.Done():
					return a.cancelCodexAppServerTurn(runtime)
				case startErr := <-startResultCh:
					startResultCh = nil
					if startErr != nil {
						return a.handleCodexAppServerTurnStartError(runtime, startErr)
					}
				}
			}
			return result, err
		}
	}
}

func (a *ACPAgent) handleCodexAppServerTurnStartError(runtime *codexAppServerTurnRuntime, err error) (string, error) {
	result, _, handledErr := a.handleCodexAppServerEvent(runtime, &codexTurnEvent{Kind: "error", Text: err.Error()})
	return result, handledErr
}

func detachCodexAppServerTurn(runtime *codexAppServerTurnRuntime) (string, error) {
	runtime.messageProgress.flush(progressCallbacks{
		onText: runtime.opts.onProgress, onEvent: runtime.opts.onProgressEvent,
	})
	log.Printf("[acp] turn observer detached without interrupt (pid=%d, thread=%s, conversation=%s, elapsed=%s)",
		runtime.pid, runtime.threadID, runtime.opts.conversationID, runtime.metrics.elapsed(time.Now()))
	return "", ErrCodexObserverDetached
}

func (a *ACPAgent) cancelCodexAppServerTurn(runtime *codexAppServerTurnRuntime) (string, error) {
	runtime.messageProgress.flush(progressCallbacks{
		onText: runtime.opts.onProgress, onEvent: runtime.opts.onProgressEvent,
	})
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
	callbacks := progressCallbacks{onText: runtime.opts.onProgress, onEvent: runtime.opts.onProgressEvent}
	runtime.messageProgress.beforeEvent(evt, callbacks)
	if handled, err := a.handleCodexAppServerInteraction(runtime, evt); handled {
		return "", false, err
	}
	if result, done, err := handleCodexAppServerTerminal(runtime, evt); done {
		return result, true, err
	}
	if evt.Progress != nil {
		runtime.diagnostics.remember(codexProgressPrefix + evt.Progress.DisplayText())
		callbacks.emit(*evt.Progress)
		return "", false, nil
	}
	if evt.Kind == "progress" {
		// 兼容无法提供结构化摘要的旧 watcher 事件。
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
	progressKind := ProgressKindApproval
	progressID := ""
	var handle func() error
	if evt.Approval != nil {
		progressText = "进展：Codex 请求权限审批。"
		progressID = strings.TrimSpace(evt.Approval.Request.RequestID)
		handle = func() error { return a.handleCodexApprovalEvent(runtime.opts.ctx, evt) }
	} else if evt.UserInput != nil {
		progressText = "进展：Codex 请求补充信息。"
		progressKind = ProgressKindUserInput
		progressID = strings.TrimSpace(evt.UserInput.Request.RequestID)
		handle = func() error { return a.handleCodexUserInputEvent(runtime.opts.ctx, evt) }
	} else {
		return false, nil
	}
	runtime.diagnostics.remember(progressText)
	progressCallbacks{onText: runtime.opts.onProgress, onEvent: runtime.opts.onProgressEvent}.emit(ProgressEvent{
		ID: progressID, Kind: progressKind, State: ProgressStateRunning, Sequence: evt.Sequence,
		Summary: strings.TrimSpace(strings.TrimPrefix(progressText, codexProgressPrefix)), Text: progressText,
	})
	if err := handle(); err != nil {
		a.abandonCodexTurnEvent(runtime.threadID, evt)
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

func collectCodexAppServerContent(runtime *codexAppServerTurnRuntime, evt *codexTurnEvent) {
	if evt.Delta != "" {
		runtime.assembler.addDelta(evt.ItemID, evt.MessagePhase, evt.Delta)
	}
	if evt.Text == "" {
		return
	}
	if evt.Kind == "item_completed" {
		runtime.assembler.addCompleted(evt.ItemID, evt.MessagePhase, evt.Text)
		runtime.messageProgress.observeCompleted(evt, progressCallbacks{
			onText: runtime.opts.onProgress, onEvent: runtime.opts.onProgressEvent,
		})
		return
	}
	runtime.assembler.addSnapshot(evt.ItemID, evt.MessagePhase, evt.Text)
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
