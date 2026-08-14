package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	codexThreadWatchReconcileInterval = 2 * time.Second
	codexThreadWatchRefreshTicks      = 5
)

type codexThreadWatchOptions struct {
	conversationID    string
	threadID          string
	targetTurnID      string
	turnCh            <-chan *codexTurnEvent
	initialEvents     []*codexTurnEvent
	appServerSequence uint64
	desktopEpoch      uint64
	desktopRevision   uint64
	replayedActions   map[string]bool
	onProgress        func(string)
	onProgressEvent   func(ProgressEvent)
	reconcile         <-chan time.Time
	refresh           func(context.Context) error
}

// WatchCodexThreadEvents 观察已运行 turn，并保留结构化进展字段。
func (a *ACPAgent) WatchCodexThreadEvents(ctx context.Context, conversationID string, threadID string, onProgress func(ProgressEvent)) (string, error) {
	return a.WatchCodexThreadEventsForTurn(ctx, conversationID, threadID, "", onProgress)
}

// WatchCodexThreadEventsForTurn 只观察调用方预留的同一个 active turn。
func (a *ACPAgent) WatchCodexThreadEventsForTurn(ctx context.Context, conversationID string, threadID string, turnID string, onProgress func(ProgressEvent)) (string, error) {
	if a.protocol != protocolCodexAppServer {
		return "", fmt.Errorf("agent is not codex app-server")
	}
	ticker := time.NewTicker(codexThreadWatchReconcileInterval)
	defer ticker.Stop()
	return a.watchCodexThreadWithReconcile(ctx, codexThreadWatchOptions{
		conversationID: conversationID, threadID: threadID, targetTurnID: strings.TrimSpace(turnID),
		onProgressEvent: onProgress, reconcile: ticker.C,
	})
}

// WatchCodexThread 接管已经运行的 Codex thread，并等待当前 turn 完成。
func (a *ACPAgent) WatchCodexThread(ctx context.Context, conversationID string, threadID string, onProgress func(delta string)) (string, error) {
	return a.WatchCodexThreadForTurn(ctx, conversationID, threadID, "", onProgress)
}

// WatchCodexThreadForTurn 是旧字符串进度接口的目标 turn 版本。
func (a *ACPAgent) WatchCodexThreadForTurn(ctx context.Context, conversationID string, threadID string, turnID string, onProgress func(delta string)) (string, error) {
	if a.protocol != protocolCodexAppServer {
		return "", fmt.Errorf("agent is not codex app-server")
	}
	ticker := time.NewTicker(codexThreadWatchReconcileInterval)
	defer ticker.Stop()
	return a.watchCodexThreadWithReconcile(ctx, codexThreadWatchOptions{
		conversationID: conversationID, threadID: threadID, targetTurnID: strings.TrimSpace(turnID),
		onProgress: onProgress, reconcile: ticker.C,
	})
}

// watchCodexThreadWithReconcile 同时消费实时事件和权威状态，避免单个终态事件缺失后永久挂起。
func (a *ACPAgent) watchCodexThreadWithReconcile(ctx context.Context, opts codexThreadWatchOptions) (string, error) {
	binding, hasBinding := a.runtimeBindingForThread(opts.conversationID, opts.threadID)
	if hasBinding {
		if binding.Runtime == CodexRuntimeUnknown {
			return "", ErrCodexRuntimeUnavailable
		}
		if binding.Runtime == CodexRuntimeConflict {
			return "", ErrCodexRuntimeConflict
		}
	}
	turnCh := make(chan *codexTurnEvent, codexTurnEventBufferSize)
	observerID := a.registerTurnObserver(opts.threadID, turnCh)
	defer a.unregisterTurnObserver(opts.threadID, observerID, turnCh)
	state, initialEvents, appServerSequence, desktopEpoch, desktopRevision, err := a.attachedCodexWatchSnapshot(ctx, opts, binding, hasBinding)
	if err == nil && strings.TrimSpace(opts.targetTurnID) != "" {
		if targetState, ok := a.attachedCodexTargetTurnState(opts); ok {
			state = targetState
			if !targetState.Active {
				// activeWatchSnapshot 可能属于同一 thread 中后来启动的 turn；不能让其
				// 进度或审批污染已指定的旧 watcher，交互需归还给新的观察者。
				a.abandonCodexInteractions(opts.threadID, initialEvents)
				initialEvents = nil
			}
		}
	}
	if err == nil && strings.TrimSpace(opts.targetTurnID) != "" {
		observedTurnID := strings.TrimSpace(state.LastTurnID)
		if state.Active {
			observedTurnID = strings.TrimSpace(state.ActiveTurnID)
		}
		if observedTurnID != "" && observedTurnID != strings.TrimSpace(opts.targetTurnID) {
			a.abandonCodexInteractions(opts.threadID, initialEvents)
			return "", fmt.Errorf("%w: expected turn %s, observed %s", ErrCodexControlChanged, opts.targetTurnID, observedTurnID)
		}
	}
	watch := opts
	if err == nil && !state.Active && a.desktopWatchAwaitingFinal(watch, state) {
		watch.targetTurnID = firstNonEmpty(strings.TrimSpace(watch.targetTurnID), strings.TrimSpace(state.LastTurnID))
	} else if err == nil && !state.Active {
		if interrupted := interruptedCodexThreadStateError(state, opts.threadID, ""); interrupted != nil {
			return "", interrupted
		}
		if failed := failedCodexThreadStateError(state); failed != nil {
			return "", failed
		}
		if state.LastAgentMessageText != "" {
			return state.LastAgentMessageText, nil
		}
		return "Codex App 本地任务已完成，但没有返回文本。", nil
	}
	if err == nil && watch.targetTurnID == "" {
		watch.targetTurnID = state.ActiveTurnID
	}
	watch.turnCh = turnCh
	watch.initialEvents = initialEvents
	watch.appServerSequence = appServerSequence
	watch.desktopEpoch = desktopEpoch
	watch.desktopRevision = desktopRevision
	watch.replayedActions = codexInteractionIDs(initialEvents)
	return a.collectAttachedCodexTurn(ctx, watch)
}

func (a *ACPAgent) abandonCodexInteractions(threadID string, events []*codexTurnEvent) {
	for _, event := range codexTurnInteractions(events) {
		a.abandonCodexTurnEvent(threadID, event)
	}
}

func (a *ACPAgent) attachedCodexWatchSnapshot(
	ctx context.Context,
	opts codexThreadWatchOptions,
	binding CodexThreadBinding,
	hasBinding bool,
) (CodexThreadState, []*codexTurnEvent, uint64, uint64, uint64, error) {
	if hasBinding && binding.Runtime == CodexRuntimeDesktop {
		if a.desktopRuntime == nil {
			return CodexThreadState{}, nil, 0, 0, 0, ErrCodexRuntimeUnavailable
		}
		state, batch, err := a.desktopRuntime.activeWatchSnapshot(opts.threadID)
		return state, batch.Events, 0, batch.Epoch, batch.Revision, err
	}
	state, snapshot, _, sequence, err := a.readCodexAppServerThreadSnapshotResult(ctx, opts.threadID)
	if err != nil || !state.Active {
		return state, nil, sequence, 0, 0, err
	}
	targetTurnID := firstNonEmpty(strings.TrimSpace(opts.targetTurnID), strings.TrimSpace(state.ActiveTurnID))
	events := projectCodexAppServerActiveTurnEvents(snapshot, targetTurnID)
	events = append(events, a.claimPendingCodexInteractions(opts.threadID)...)
	return state, events, sequence, 0, 0, nil
}

func (a *ACPAgent) collectAttachedCodexTurn(ctx context.Context, opts codexThreadWatchOptions) (string, error) {
	assembler, diagnostics := newCodexFinalAssembler(), newCodexTurnDiagnostics(codexTurnDiagnosticsLimit)
	messageProgress := codexMessageProgressBuffer{}
	callbacks := progressCallbacks{onText: opts.onProgress, onEvent: opts.onProgressEvent}
	processEvent := func(evt *codexTurnEvent) (string, bool, error) {
		if evt == nil {
			return "", false, nil
		}
		if evt.TurnID != "" && opts.targetTurnID != "" && evt.TurnID != opts.targetTurnID {
			if isCodexTurnInteractionEvent(evt) {
				a.abandonCodexTurnEvent(opts.threadID, evt)
				return "", true, fmt.Errorf(
					"%w: expected turn %s, observed interaction for %s",
					ErrCodexControlChanged, opts.targetTurnID, evt.TurnID,
				)
			}
			return "", false, nil
		}
		messageProgress.beforeEvent(evt, callbacks)
		if evt.Kind == "interrupted" {
			return "", true, attachedCodexInterruptedError(opts, evt)
		}
		if evt.Approval != nil {
			if err := a.handleAttachedCodexApproval(ctx, evt); err != nil {
				a.abandonCodexTurnEvent(opts.threadID, evt)
				if errors.Is(err, errCodexApprovalResponsePending) {
					return "", false, nil
				}
				return "", true, err
			}
			return "", false, nil
		}
		if evt.UserInput != nil {
			if err := a.handleCodexUserInputEvent(ctx, evt); err != nil {
				a.abandonCodexTurnEvent(opts.threadID, evt)
				return "", true, fmt.Errorf("user input response error: %w", err)
			}
			return "", false, nil
		}
		if evt.Kind == "error" {
			return "", true, fmt.Errorf("%w: %s", ErrCodexTurnTerminal, diagnostics.withError(evt.Text))
		}
		collectCodexTurnText(assembler, evt, callbacks, diagnostics, &messageProgress)
		if evt.Kind == "completed" {
			text, err := a.attachedCodexFinalText(ctx, opts.conversationID, opts.threadID, opts.targetTurnID, assembler)
			return text, true, err
		}
		return "", false, nil
	}
	for _, evt := range opts.initialEvents {
		if text, done, err := processEvent(evt); done || err != nil {
			return text, err
		}
	}
	ticksWithoutEvent := 0
	for {
		select {
		case <-ctx.Done():
			messageProgress.flush(callbacks)
			return "", ctx.Err()
		case <-opts.reconcile:
			ticksWithoutEvent++
			refreshed := false
			if ticksWithoutEvent >= codexThreadWatchRefreshTicks {
				refresh := opts.refresh
				if refresh == nil {
					refresh = func(ctx context.Context) error {
						return a.refreshAttachedCodexThread(ctx, opts.conversationID, opts.threadID)
					}
				}
				if err := refresh(ctx); err != nil {
					return "", err
				}
				ticksWithoutEvent = 0
				refreshed = true
			}
			text, finished, err := a.reconcileAttachedCodexTurn(ctx, opts, assembler, refreshed)
			if err != nil {
				messageProgress.flush(callbacks)
				return text, err
			}
			if finished {
				messageProgress.discard()
				return text, err
			}
		case evt := <-opts.turnCh:
			if shouldSkipCodexAppServerSnapshotDuplicate(evt, opts.appServerSequence) {
				continue
			}
			if shouldSkipCodexDesktopReplayDuplicate(evt, opts.desktopEpoch, opts.desktopRevision, opts.replayedActions) {
				continue
			}
			ticksWithoutEvent = 0
			if text, done, err := processEvent(evt); done || err != nil {
				return text, err
			}
		}
	}
}

func shouldSkipCodexAppServerSnapshotDuplicate(event *codexTurnEvent, replaySequence uint64) bool {
	if event == nil || replaySequence == 0 || event.Sequence == 0 || event.Sequence > replaySequence ||
		isCodexTurnControlEvent(event) || event.Progress != nil {
		return false
	}
	return event.Delta != "" || event.Text != "" || event.Kind == "item_completed" || event.Kind == "activity"
}

func codexInteractionIDs(events []*codexTurnEvent) map[string]bool {
	result := make(map[string]bool)
	for _, event := range events {
		if id := codexInteractionID(event); id != "" {
			result[id] = true
		}
	}
	return result
}

func codexInteractionID(event *codexTurnEvent) string {
	if event == nil {
		return ""
	}
	if event.Approval != nil {
		return strings.TrimSpace(event.Approval.Request.RequestID)
	}
	if event.UserInput != nil {
		return strings.TrimSpace(event.UserInput.Request.RequestID)
	}
	return ""
}

func shouldSkipCodexDesktopReplayDuplicate(event *codexTurnEvent, replayEpoch uint64, replayRevision uint64, replayedActions map[string]bool) bool {
	if event == nil || replayEpoch == 0 || event.DesktopEpoch == 0 {
		return false
	}
	if event.DesktopEpoch < replayEpoch {
		return true
	}
	if event.DesktopEpoch > replayEpoch || replayRevision == 0 || event.DesktopRevision == 0 || event.DesktopRevision > replayRevision {
		return false
	}
	if id := codexInteractionID(event); id != "" {
		return replayedActions[id]
	}
	return true
}

// attachedCodexInterruptedError 保留 watcher 后续核对 rollout 所需的 thread 和 turn 身份。
func attachedCodexInterruptedError(opts codexThreadWatchOptions, evt *codexTurnEvent) error {
	return &CodexTurnInterruptedError{
		ThreadID: opts.threadID,
		TurnID:   firstNonEmpty(evt.TurnID, opts.targetTurnID),
	}
}

// refreshAttachedCodexThread 在 Desktop 事件静默时主动拉取带 revision 屏障的目标状态。
func (a *ACPAgent) refreshAttachedCodexThread(ctx context.Context, conversationID string, threadID string) error {
	binding, ok := a.runtimeBindingForThread(conversationID, threadID)
	if !ok || binding.Runtime != CodexRuntimeDesktop || a.desktopRuntime == nil {
		return nil
	}
	return a.desktopRuntime.LoadHistory(ctx, CodexThreadRef{
		ConversationID: conversationID, ThreadID: threadID,
	})
}

// reconcileAttachedCodexTurn 在实时事件缺失时根据当前 active turn 判断原任务是否已经结束。
func (a *ACPAgent) reconcileAttachedCodexTurn(
	ctx context.Context,
	opts codexThreadWatchOptions,
	assembler *codexFinalAssembler,
	refreshed bool,
) (string, bool, error) {
	if state, ok := a.attachedCodexTargetTurnState(opts); ok {
		if state.Active {
			return "", false, nil
		}
		if interrupted := interruptedCodexThreadStateError(state, opts.threadID, opts.targetTurnID); interrupted != nil {
			return "", true, interrupted
		}
		if failed := failedCodexThreadStateError(state); failed != nil {
			return "", true, failed
		}
		if a.desktopWatchAwaitingFinal(opts, state) && !refreshed {
			return "", false, nil
		}
		if text := assembler.finalText(); text != "" {
			return text, true, nil
		}
		if state.LastAgentMessageText != "" {
			return state.LastAgentMessageText, true, nil
		}
		if isCodexDesktopTerminalStatus(state.LastTurnStatus) {
			return "Codex App 本地任务已完成，但没有返回文本。", true, nil
		}
	}
	state, err := a.ReadCodexThreadState(ctx, opts.conversationID, opts.threadID)
	if err != nil {
		return "", false, err
	}
	if err := validateCodexWatchTurnIdentity(opts.targetTurnID, state); err != nil {
		return "", true, err
	}
	if state.Active && (opts.targetTurnID == "" || state.ActiveTurnID == "" || state.ActiveTurnID == opts.targetTurnID) {
		return "", false, nil
	}
	if interrupted := interruptedCodexThreadStateError(state, opts.threadID, opts.targetTurnID); interrupted != nil {
		return "", true, interrupted
	}
	if failed := failedCodexThreadStateError(state); failed != nil {
		return "", true, failed
	}
	if a.desktopWatchAwaitingFinal(opts, state) && !refreshed {
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

func (a *ACPAgent) attachedCodexTargetTurnState(opts codexThreadWatchOptions) (CodexThreadState, bool) {
	turnID := strings.TrimSpace(opts.targetTurnID)
	if turnID == "" || a.desktopRuntime == nil {
		return CodexThreadState{}, false
	}
	binding, ok := a.runtimeBindingForThread(opts.conversationID, opts.threadID)
	if !ok || binding.Runtime != CodexRuntimeDesktop {
		return CodexThreadState{}, false
	}
	return a.desktopRuntime.targetTurnState(opts.threadID, turnID)
}

func validateCodexWatchTurnIdentity(targetTurnID string, state CodexThreadState) error {
	targetTurnID = strings.TrimSpace(targetTurnID)
	if targetTurnID == "" {
		return nil
	}
	observedTurnID := strings.TrimSpace(state.LastTurnID)
	if state.Active {
		observedTurnID = strings.TrimSpace(state.ActiveTurnID)
	}
	if observedTurnID == "" || observedTurnID == targetTurnID {
		return nil
	}
	return fmt.Errorf("%w: expected turn %s, observed %s", ErrCodexControlChanged, targetTurnID, observedTurnID)
}

func (a *ACPAgent) desktopWatchAwaitingFinal(opts codexThreadWatchOptions, state CodexThreadState) bool {
	binding, ok := a.runtimeBindingForThread(opts.conversationID, opts.threadID)
	if !ok || binding.Runtime != CodexRuntimeDesktop || a.desktopRuntime == nil {
		return false
	}
	turnID := firstNonEmpty(strings.TrimSpace(opts.targetTurnID), strings.TrimSpace(state.LastTurnID))
	return a.desktopRuntime.awaitingFinalAnswer(opts.threadID, turnID)
}

// interruptedCodexThreadStateError 从权威快照识别漏收事件后的中断终态。
func interruptedCodexThreadStateError(state CodexThreadState, threadID string, targetTurnID string) error {
	if !isCodexInterruptedStatus(state.LastTurnStatus) {
		return nil
	}
	if targetTurnID != "" && state.LastTurnID != "" && state.LastTurnID != targetTurnID {
		return nil
	}
	return &CodexTurnInterruptedError{
		ThreadID: firstNonEmpty(state.ThreadID, threadID),
		TurnID:   firstNonEmpty(state.LastTurnID, targetTurnID),
	}
}

func failedCodexThreadStateError(state CodexThreadState) error {
	if state.LastTurnStatus != "failed" {
		return nil
	}
	message := strings.TrimSpace(state.LastTurnError)
	if message == "" {
		message = "Codex App 本地任务执行失败"
	}
	return fmt.Errorf("%w: %s", ErrCodexTurnTerminal, message)
}

// isCodexInterruptedStatus 兼容 app-server 与 Desktop 使用的中断状态名称。
func isCodexInterruptedStatus(status string) bool {
	switch status {
	case "interrupted", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func (a *ACPAgent) handleAttachedCodexApproval(ctx context.Context, evt *codexTurnEvent) error {
	if err := a.handleCodexApprovalEvent(ctx, evt); err != nil {
		return fmt.Errorf("approval response error: %w", err)
	}
	return nil
}

func collectCodexTurnText(
	assembler *codexFinalAssembler,
	evt *codexTurnEvent,
	callbacks progressCallbacks,
	diagnostics *codexTurnDiagnostics,
	messageProgress *codexMessageProgressBuffer,
) {
	if evt.Progress != nil {
		diagnostics.remember(codexProgressPrefix + evt.Progress.DisplayText())
		callbacks.emit(*evt.Progress)
		return
	}
	if evt.Delta != "" {
		assembler.addDelta(evt.ItemID, evt.MessagePhase, evt.Delta)
	}
	if evt.Text != "" {
		if evt.Kind == "item_completed" {
			assembler.addCompleted(evt.ItemID, evt.MessagePhase, evt.Text)
			messageProgress.observeCompleted(evt, callbacks)
		} else {
			assembler.addSnapshot(evt.ItemID, evt.MessagePhase, evt.Text)
		}
	}
}

func (a *ACPAgent) attachedCodexFinalText(ctx context.Context, conversationID string, threadID string, targetTurnID string, assembler *codexFinalAssembler) (string, error) {
	if text := assembler.finalText(); text != "" {
		return text, nil
	}
	if state, ok := a.attachedCodexTargetTurnState(codexThreadWatchOptions{
		conversationID: conversationID, threadID: threadID, targetTurnID: targetTurnID,
	}); ok && state.LastAgentMessageText != "" {
		return state.LastAgentMessageText, nil
	}
	state, err := a.ReadCodexThreadState(ctx, conversationID, threadID)
	if err == nil && state.LastAgentMessageText != "" {
		return state.LastAgentMessageText, nil
	}
	return "Codex App 本地任务已完成，但没有返回文本。", nil
}
