package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

// discardDetachedWithoutTerminal 停止已被投递栅栏撤销的进度会话，并清除其
// durable recovery；它不再更新原卡片，供账号撤权等禁止继续出站的路径使用。
func (s *progressSession) discardDetachedWithoutTerminal() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.detachClaimed = false
	s.finished = true
	s.terminalClaimed = true
	s.stateMu.Unlock()
	s.stopBackground()
	s.ioMu.Lock()
	s.discardActiveStreamRecoveryLocked()
	s.ioMu.Unlock()
}

// detachWithoutTerminal 冻结当前进度展示，但不把共享任务宣告为完成、失败或停止。
func (s *progressSession) detachWithoutTerminal(ctx context.Context, notice string) error {
	if s == nil {
		return nil
	}
	notice = strings.TrimSpace(notice)
	if notice == "" {
		notice = "已解除当前窗口的会话绑定；本地 Codex 任务继续运行。"
	}
	s.ioMu.Lock()
	s.stateMu.Lock()
	if s.detachClaimed {
		s.detachClaimed = false
	} else if s.finished || s.terminalClaimed {
		s.stateMu.Unlock()
		s.ioMu.Unlock()
		return nil
	}
	stream := s.stream
	reply := s.reply
	reservationID := s.recoveryReservation
	s.stateMu.Unlock()

	var supersedable platform.SupersedableStream
	var detachable platform.DetachableStream
	if stream != nil {
		supersedable, _ = stream.(platform.SupersedableStream)
		detachable, _ = stream.(platform.DetachableStream)
		if supersedable == nil && detachable == nil {
			s.ioMu.Unlock()
			return platform.ErrUnsupported
		}
	}
	type durableDetach struct {
		outbox       *terminalOutbox
		reservation  string
		transitionID string
		checkpoint   platform.SupersedeCheckpoint
		prepared     platform.PreparedSupersedableStream
	}
	var durable *durableDetach
	if stream != nil && s.handler != nil && reservationID != "" {
		outbox := s.handler.currentTerminalOutbox()
		reporter, routeOK := optionalDeliveryRouteReporter(reply)
		exporter, exportOK := stream.(platform.DurableStreamReferenceExporter)
		detachPreparer, detachPrepareOK := optionalDurableStreamDetachPreparer(reply)
		supersedePreparer, supersedePrepareOK := optionalDurableStreamSupersedePreparer(reply)
		prepared, deliverOK := stream.(platform.PreparedSupersedableStream)
		prepareOK := detachPrepareOK || supersedePrepareOK
		if outbox != nil && routeOK && reporter.DeliveryRoute().Valid() && exportOK && prepareOK && deliverOK {
			reference, err := exporter.DurableReference()
			if err != nil {
				s.ioMu.Unlock()
				return fmt.Errorf("export released progress card: %w", err)
			}
			transitionID := uuid.NewString()
			var checkpoint platform.SupersedeCheckpoint
			if detachPrepareOK {
				checkpoint, err = detachPreparer.PrepareDetachFromReference(reference, notice, transitionID)
			} else {
				checkpoint, err = supersedePreparer.PrepareSupersedeFromReference(reference, notice, transitionID)
			}
			if err != nil {
				s.ioMu.Unlock()
				return fmt.Errorf("prepare released progress card freeze: %w", err)
			}
			if err := outbox.beginStreamReanchor(reservationID); err != nil {
				s.ioMu.Unlock()
				return fmt.Errorf("reserve released progress card freeze: %w", err)
			}
			pending := pendingStreamSupersede{
				ID: transitionID, Route: reporter.DeliveryRoute(), Checkpoint: checkpoint,
			}
			if err := outbox.detachStreamReservation(reservationID, pending); err != nil {
				outbox.endStreamReanchor(reservationID)
				s.ioMu.Unlock()
				return fmt.Errorf("persist released progress card freeze: %w", err)
			}
			durable = &durableDetach{
				outbox: outbox, reservation: reservationID,
				transitionID: transitionID, checkpoint: checkpoint, prepared: prepared,
			}
			s.stateMu.Lock()
			s.recoveryReservation = ""
			s.stateMu.Unlock()
		}
	}
	s.stateMu.Lock()
	s.finished = true
	s.terminalClaimed = true
	s.stateMu.Unlock()
	s.ioMu.Unlock()

	s.stopBackground()
	if supersedable == nil && detachable == nil {
		s.ioMu.Lock()
		s.discardActiveStreamRecoveryLocked()
		s.ioMu.Unlock()
		return nil
	}
	detachCtx, cancel := context.WithTimeout(context.WithoutCancel(normalizeContext(ctx)), 5*time.Second)
	defer cancel()
	if durable != nil {
		err := durable.prepared.DeliverPreparedSupersede(detachCtx, durable.checkpoint)
		if err != nil {
			if recordErr := durable.outbox.recordPendingStreamSupersedeFailure(
				durable.reservation, durable.transitionID, err,
			); recordErr != nil && !errors.Is(recordErr, err) {
				log.Printf("[terminal-outbox] failed to persist released card freeze retry: %v", recordErr)
			}
			durable.outbox.endStreamReanchor(durable.reservation)
			return nil
		}
		if err := durable.outbox.completePendingStreamSupersede(durable.reservation, durable.transitionID); err != nil {
			log.Printf("[terminal-outbox] failed to clear delivered released card freeze: %v", err)
		}
		durable.outbox.endStreamReanchor(durable.reservation)
		return nil
	}
	var err error
	if detachable != nil {
		err = detachable.Detach(detachCtx, notice)
	} else {
		err = supersedable.Supersede(detachCtx, notice)
	}
	if err != nil {
		return err
	}
	s.ioMu.Lock()
	s.discardActiveStreamRecoveryLocked()
	s.ioMu.Unlock()
	return nil
}

type preparedProgressTerminal struct {
	checkpoint   *platform.TerminalCheckpoint
	consumed     bool
	notification string
	reply        platform.Replier
}

// canPrepareDurableTerminal 只在没有原生 stream，或 adapter 能导出 checkpoint 时进入 outbox 路径。
func (s *progressSession) canPrepareDurableTerminal() bool {
	if s == nil {
		return true
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.stream == nil {
		return true
	}
	_, ok := s.stream.(platform.DurableTerminalStream)
	return ok
}

func (s *progressSession) hasDurableTerminalStream() bool {
	if s == nil {
		return false
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.stream == nil {
		return false
	}
	_, ok := s.stream.(platform.DurableTerminalStream)
	return ok
}

func (s *progressSession) terminalResultPresentation() (string, bool) {
	if s == nil {
		return "", false
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.terminalResultPresentationStateLocked()
}

func (s *progressSession) terminalResultPresentationStateLocked() (string, bool) {
	if s == nil || s.cfg.Mode != progressModeStream || s.stream == nil || s.reply == nil ||
		!s.reply.Capabilities().Streaming || !s.reply.Capabilities().FinalReplyOutsideStream {
		return "", false
	}
	return progressResultTitleForAgentWorkspace(s.agentName, s.workspaceRoot, 60), true
}

func (s *progressSession) prepareDurableTerminal(replyWriter platform.Replier, finalText string, failed bool, stopped bool) (preparedProgressTerminal, error) {
	if s == nil {
		return preparedProgressTerminal{}, nil
	}
	parentCanceled := s.stopBackground()
	stopped = stopped || parentCanceled
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.stateMu.Lock()
	stream := s.stream
	currentReply := s.reply
	if stream == nil || currentReply == nil || !currentReply.Capabilities().Streaming {
		s.stateMu.Unlock()
		return preparedProgressTerminal{}, nil
	}
	durable, ok := stream.(platform.DurableTerminalStream)
	if !ok {
		s.stateMu.Unlock()
		return preparedProgressTerminal{}, platform.ErrUnsupported
	}
	if currentReply == nil {
		currentReply = replyWriter
	}
	content, terminalFailed, consumed := progressTerminalArguments(currentReply, parentCanceled, finalText, failed, stopped)
	if strings.TrimSpace(content) == "" {
		content = s.latestTaskSnapshotContentStateLocked()
	}
	s.terminalClaimed = true
	s.finished = true
	s.stateMu.Unlock()

	var checkpoint platform.TerminalCheckpoint
	var err error
	if stateful, ok := stream.(platform.StatefulDurableTerminalStream); ok {
		state := platform.StreamTerminalCompleted
		switch {
		case stopped:
			state = platform.StreamTerminalStopped
		case terminalFailed:
			state = platform.StreamTerminalFailed
		}
		checkpoint, err = stateful.PrepareTerminalWithState(content, state)
	} else {
		checkpoint, err = durable.PrepareTerminal(content, terminalFailed)
	}
	if err != nil {
		return preparedProgressTerminal{}, err
	}
	return preparedProgressTerminal{
		checkpoint:   &checkpoint,
		consumed:     consumed,
		notification: renderStreamTerminalNotification(parentCanceled, failed, stopped, finalText),
		reply:        currentReply,
	}, nil
}

func progressTerminalArguments(replyWriter platform.Replier, parentCanceled bool, finalText string, failed bool, stopped bool) (string, bool, bool) {
	stopped = stopped || parentCanceled
	terminalFailed := failed && !stopped
	if finalText == progressStatusOnlyComplete {
		return "", terminalFailed, false
	}
	if shouldKeepFinalReplyOutsideStream(replyWriter, finalText) {
		return "", terminalFailed, false
	}
	if stopped {
		return firstNonBlank(finalText, "任务已按请求停止。"), false, strings.TrimSpace(finalText) != ""
	}
	if !canConsumeFinalReplyInStream(finalText) {
		fallback := progressDefaultCompletion
		if failed {
			fallback = "任务执行失败。"
		}
		return fallback, terminalFailed, false
	}
	content := strings.TrimSpace(finalText)
	if content == "" {
		content = progressDefaultCompletion
	}
	return content, terminalFailed, strings.TrimSpace(finalText) != "" && finalText != progressStatusOnlyComplete
}

func (s *progressSession) continueProgressStreamLocked(snapshot progressCardSnapshot) (bool, error) {
	s.stateMu.Lock()
	stream := s.stream
	reply := s.reply
	nextSegment := s.segmentNumber + 1
	s.stateMu.Unlock()
	if _, ok := stream.(platform.SupersedableStream); !ok {
		return false, fmt.Errorf("oversized progress stream does not support continuation")
	}
	segmentStart := len(snapshot.timelineItems) - 1
	if nextSegment < 2 {
		nextSegment = 2
	}
	baseTitle := progressTaskTitleForAgentWorkspace(s.agentName, s.workspaceRoot, s.taskText, 60)
	oldContent := trimActiveThinkingIndicator(s.lastContent)
	if oldContent == "" {
		oldContent = renderInitialCardProgress()
	}
	segmentSnapshot := snapshot
	segmentSnapshot.timelineItems = append([]agent.ProgressEvent(nil), snapshot.timelineItems[segmentStart:]...)
	result, err := s.reanchorStreamLocked(progressReanchorRequest{
		Context:      s.ctx,
		Reply:        reply,
		Snapshot:     segmentSnapshot,
		Title:        fmt.Sprintf("%s · 进度 %d", baseTitle, nextSegment),
		TransitionID: uuid.NewString(),
		Notice:       fmt.Sprintf("%s\n\n---\n后续进度见第 %d 张卡片。", oldContent, nextSegment),
	})
	if !result.Moved {
		return false, err
	}
	segmentAnchor := snapshot.timelineItems[segmentStart]
	s.stateMu.Lock()
	s.segmentAnchor = &segmentAnchor
	s.segmentNumber = nextSegment
	s.stateMu.Unlock()
	if err != nil {
		log.Printf("[handler] continued progress card but failed to freeze previous segment: %v", err)
	}
	if result.SupersedePending {
		log.Printf("[handler] continued progress card; previous segment supersede is queued for retry")
	}
	return true, nil
}

func (s *progressSession) registerStreamRecoveryChangeHandlerLocked(stream platform.Stream) {
	notifier, ok := stream.(platform.DurableStreamReferenceChangeNotifier)
	if !ok {
		return
	}
	notifier.SetDurableReferenceChangeHandler(s.refreshActiveStreamRecovery)
}

func (s *progressSession) refreshActiveStreamRecovery() {
	if s == nil {
		return
	}
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.stateMu.Lock()
	if s.finished || s.terminalClaimed || s.stream == nil {
		s.stateMu.Unlock()
		return
	}
	stream := s.stream
	reply := s.reply
	s.stateMu.Unlock()
	if err := s.persistActiveStreamRecoveryLocked(stream, reply); err != nil {
		log.Printf("[terminal-outbox] failed to refresh active card recovery after adapter state change: %v", err)
	}
}

func (s *progressSession) persistActiveStreamRecoveryLocked(stream platform.Stream, reply platform.Replier) error {
	if s == nil || s.handler == nil {
		return nil
	}
	outbox := s.handler.currentTerminalOutbox()
	reporter, routeOK := optionalDeliveryRouteReporter(reply)
	exporter, streamOK := stream.(platform.DurableStreamReferenceExporter)
	if outbox == nil || !routeOK || !reporter.DeliveryRoute().Valid() || !streamOK {
		return nil
	}
	reference, err := exporter.DurableReference()
	if err != nil || strings.TrimSpace(reference.Kind) == "" {
		if err == nil {
			err = fmt.Errorf("durable stream reference kind is empty")
		}
		return fmt.Errorf("export active progress card: %w", err)
	}
	s.stateMu.Lock()
	reservationID := s.recoveryReservation
	resultTitle, richResult := s.terminalResultPresentationStateLocked()
	guard := s.deliveryGuard
	s.stateMu.Unlock()
	if reservationID != "" {
		return outbox.refreshStreamReservation(reservationID, reporter.DeliveryRoute(), reference)
	}
	trace, _ := observability.TraceFromContext(s.ctx)
	draft := terminalOutboxDraft{
		Route: reporter.DeliveryRoute(), AgentName: s.agentName, Stopped: true,
		Stream: &reference, ResultTitle: resultTitle, RichResult: richResult,
		Text: activeStreamRestartText, Trace: trace, ActiveStreamRecovery: true,
	}
	guard.apply(&draft)
	reservation, err := outbox.reserve(draft)
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	s.recoveryReservation = reservation.ID
	s.stateMu.Unlock()
	return nil
}

func (s *progressSession) activeRecoveryReservation() string {
	if s == nil {
		return ""
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.recoveryReservation
}

func (s *progressSession) refreshActiveStreamRecoveryTrace(trace observability.TraceContext) error {
	if s == nil || s.handler == nil {
		return nil
	}
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	reservationID := s.activeRecoveryReservation()
	if reservationID == "" {
		return nil
	}
	outbox := s.handler.currentTerminalOutbox()
	if outbox == nil {
		return nil
	}
	return outbox.refreshStreamReservationTrace(reservationID, trace)
}

// reanchor 在消息底部创建新任务卡，并把后续进展与终态原子切换到新流。
func (s *progressSession) reanchor(ctx context.Context, reply platform.Replier, snapshot progressCardSnapshot, transitionID string) (progressReanchorResult, error) {
	if s == nil || reply == nil || !reply.Capabilities().Streaming {
		return progressReanchorResult{}, nil
	}
	reply = progressReplier(reply)
	if reply == nil || !reply.Capabilities().Streaming {
		return progressReanchorResult{}, nil
	}

	moveCtx, cancel := context.WithTimeout(context.WithoutCancel(normalizeContext(ctx)), 5*time.Second)
	defer cancel()
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	return s.reanchorStreamLocked(progressReanchorRequest{
		Context:      moveCtx,
		Reply:        reply,
		Snapshot:     snapshot,
		Title:        progressTaskTitleForAgentWorkspace(s.agentName, s.workspaceRoot, s.taskText, 60),
		TransitionID: transitionID,
		Notice:       progressSupersededNotice,
	})
}

// reanchorStreamLocked 必须在持有 ioMu 时调用。只有 durable 事务落盘成功后，
// 才会改变 task-card binding 和内存中的权威 stream。
func (s *progressSession) reanchorStreamLocked(request progressReanchorRequest) (progressReanchorResult, error) {
	s.stateMu.Lock()
	if s.finished || s.terminalClaimed || s.stream == nil {
		s.stateMu.Unlock()
		return progressReanchorResult{}, nil
	}
	currentStream := s.stream
	currentReply := s.reply
	reservationID := s.recoveryReservation
	latestSnapshot := s.latestTaskSnapshot

	initialContent := renderInitialCardProgress()
	var initialPresentation *platform.StreamPresentation
	withPrefix := request.Snapshot.withPrefix
	if request.Snapshot.effectiveProgress || request.Snapshot.structured {
		snapshot := request.Snapshot
		if strings.TrimSpace(snapshot.text) == "" {
			snapshot.text = snapshot.summary
		}
		initialContent = s.activeSnapshotContentStateLocked(snapshot)
		p := s.snapshotPresentationStateLocked(snapshot)
		initialPresentation = &p
	} else if latestSnapshot.effectiveProgress {
		withPrefix = latestSnapshot.withPrefix
		initialContent = s.activeSnapshotContentStateLocked(latestSnapshot)
		p := s.snapshotPresentationStateLocked(latestSnapshot)
		initialPresentation = &p
	} else if strings.TrimSpace(latestSnapshot.text) == "" && strings.TrimSpace(request.Snapshot.summary) != "" {
		initialContent = appendActiveThinkingIndicator(request.Snapshot.summary)
	} else if strings.TrimSpace(latestSnapshot.text) == "" && trimActiveThinkingIndicator(s.lastContent) != "" {
		initialContent = appendActiveThinkingIndicator(trimActiveThinkingIndicator(s.lastContent))
	}
	s.stateMu.Unlock()

	oldStream, ok := currentStream.(platform.SupersedableStream)
	if !ok {
		return progressReanchorResult{}, nil
	}
	ctx := normalizeContext(request.Context)
	reply := request.Reply
	if reply == nil || !reply.Capabilities().Streaming {
		return progressReanchorResult{}, nil
	}
	title := strings.TrimSpace(request.Title)
	if title == "" {
		title = progressTaskTitleForAgentWorkspace(s.agentName, s.workspaceRoot, s.taskText, 60)
	}
	notice := strings.TrimSpace(request.Notice)
	if notice == "" {
		notice = progressSupersededNotice
	}
	if withPrefix {
		initialContent = s.prefix + initialContent
		if initialPresentation != nil {
			initialPresentation.Preview = s.prefix + initialPresentation.Preview
			initialPresentation.Details = s.prefix + initialPresentation.Details
		}
	}

	type durableReanchor struct {
		outbox       *terminalOutbox
		reservation  string
		oldRoute     platform.DeliveryRoute
		newRoute     platform.DeliveryRoute
		checkpoint   platform.SupersedeCheckpoint
		prepared     platform.PreparedSupersedableStream
		transitionID string
	}
	var durable *durableReanchor
	var outbox *terminalOutbox
	if s.handler != nil {
		outbox = s.handler.currentTerminalOutbox()
	}
	oldRouteReporter, oldRouteOK := optionalDeliveryRouteReporter(currentReply)
	newRouteReporter, newRouteOK := optionalDeliveryRouteReporter(reply)
	oldExporter, oldExportOK := currentStream.(platform.DurableStreamReferenceExporter)
	preparer, prepareOK := optionalDurableStreamSupersedePreparer(currentReply)
	preparedStream, preparedOK := currentStream.(platform.PreparedSupersedableStream)
	if outbox != nil && reservationID != "" && oldRouteOK && newRouteOK && oldExportOK && prepareOK && preparedOK {
		oldRoute := oldRouteReporter.DeliveryRoute()
		newRoute := newRouteReporter.DeliveryRoute()
		if oldRoute.Valid() && newRoute.Valid() {
			transitionID := strings.TrimSpace(request.TransitionID)
			if _, err := uuid.Parse(transitionID); err != nil {
				return progressReanchorResult{}, fmt.Errorf("invalid progress reanchor transition ID")
			}
			if err := outbox.beginStreamReanchor(reservationID); err != nil {
				return progressReanchorResult{}, fmt.Errorf("reserve progress reanchor delivery: %w", err)
			}
			defer outbox.endStreamReanchor(reservationID)
			oldReference, err := oldExporter.DurableReference()
			if err != nil {
				return progressReanchorResult{}, fmt.Errorf("export previous progress card: %w", err)
			}
			checkpoint, err := preparer.PrepareSupersedeFromReference(oldReference, notice, transitionID)
			if err != nil {
				return progressReanchorResult{}, fmt.Errorf("prepare previous progress card supersede: %w", err)
			}
			durable = &durableReanchor{
				outbox: outbox, reservation: reservationID,
				oldRoute: oldRoute, newRoute: newRoute, checkpoint: checkpoint,
				prepared: preparedStream, transitionID: transitionID,
			}
		}
	}

	stream, err := reply.OpenStream(ctx, platform.StreamOptions{
		Title:               title,
		InitialContent:      initialContent,
		InitialPresentation: initialPresentation,
	})
	if err != nil {
		return progressReanchorResult{}, err
	}
	if stream == nil {
		return progressReanchorResult{}, fmt.Errorf("progress reanchor returned nil stream")
	}
	if durable != nil {
		newExporter, ok := stream.(platform.DurableStreamReferenceExporter)
		if !ok {
			durable = nil
		} else {
			newReference, exportErr := newExporter.DurableReference()
			if exportErr != nil {
				s.cleanupFailedReanchorStream(ctx, stream)
				return progressReanchorResult{}, fmt.Errorf("export reanchored progress card: %w", exportErr)
			}
			pending := pendingStreamSupersede{
				ID: durable.transitionID, Route: durable.oldRoute,
				Checkpoint: durable.checkpoint,
			}
			if persistErr := durable.outbox.reanchorStreamReservation(
				durable.reservation, durable.newRoute, newReference, pending,
			); persistErr != nil {
				s.cleanupFailedReanchorStream(ctx, stream)
				return progressReanchorResult{}, fmt.Errorf("persist reanchored progress card recovery: %w", persistErr)
			}
		}
	}
	if durable == nil {
		if err := s.persistActiveStreamRecoveryLocked(stream, reply); err != nil {
			s.cleanupFailedReanchorStream(ctx, stream)
			return progressReanchorResult{}, fmt.Errorf("persist reanchored progress card recovery: %w", err)
		}
	}
	rebindProgressTaskCard(currentReply, reply)
	s.stateMu.Lock()
	s.stream = stream
	s.reply = reply
	s.stateMu.Unlock()
	s.streamOpenAttempted = true
	s.lastContent = initialContent
	s.registerStreamRecoveryChangeHandlerLocked(stream)
	if durable == nil {
		if err := oldStream.Supersede(ctx, notice); err != nil {
			return progressReanchorResult{Moved: true}, err
		}
		return progressReanchorResult{Moved: true}, nil
	}
	if err := durable.prepared.DeliverPreparedSupersede(ctx, durable.checkpoint); err != nil {
		if recordErr := durable.outbox.recordPendingStreamSupersedeFailure(durable.reservation, durable.transitionID, err); recordErr != nil && !errors.Is(recordErr, err) {
			log.Printf("[terminal-outbox] failed to persist progress card supersede retry: %v", recordErr)
		}
		return progressReanchorResult{Moved: true, SupersedePending: true}, nil
	}
	if err := durable.outbox.completePendingStreamSupersede(durable.reservation, durable.transitionID); err != nil {
		log.Printf("[terminal-outbox] failed to clear delivered progress card supersede: %v", err)
		return progressReanchorResult{Moved: true, SupersedePending: true}, nil
	}
	return progressReanchorResult{Moved: true}, nil
}

func (s *progressSession) cleanupFailedReanchorStream(ctx context.Context, stream platform.Stream) {
	supersedable, ok := stream.(platform.SupersedableStream)
	if !ok {
		return
	}
	if err := supersedable.Supersede(ctx, "任务卡迁移失败，后续进展仍保留在原卡。"); err != nil {
		log.Printf("[handler] failed to mark abandoned progress card: %v", err)
	}
}

func rebindProgressTaskCard(previous platform.Replier, current platform.Replier) {
	reporter, ok := current.(platform.TaskCardReporter)
	if !ok {
		return
	}
	cardID := strings.TrimSpace(reporter.CurrentTaskCardID())
	if cardID == "" {
		return
	}
	if binder, ok := previous.(platform.TaskCardBinder); ok {
		binder.BindTaskCard(cardID)
	}
}

func (s *progressSession) finishStream(parentCanceled bool, finalText string, failed bool, stopped bool) bool {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.stateMu.Lock()
	if s.finished || s.terminalClaimed {
		s.stateMu.Unlock()
		return false
	}
	stream := s.stream
	reply := s.reply
	if stream == nil || reply == nil || !reply.Capabilities().Streaming {
		s.stateMu.Unlock()
		return false
	}
	terminalCardContent := ""
	if reply.Capabilities().FinalReplyOutsideStream {
		terminalCardContent = s.latestTaskSnapshotContentStateLocked()
	}
	s.terminalClaimed = true
	s.finished = true
	s.stateMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped = stopped || parentCanceled
	if err := deliverFinishStream(ctx, stream, finalText, failed, stopped, terminalCardContent); err != nil {
		log.Printf("[handler] failed to finish progress stream: %v", err)
		return false
	}
	s.discardActiveStreamRecoveryLocked()
	notification := renderStreamTerminalNotification(parentCanceled, failed, stopped, finalText)
	if notification != "" && reply.Capabilities().StreamCompletionNotification {
		if notifyErr := reply.SendText(ctx, notification); notifyErr != nil {
			log.Printf("[handler] failed to send stream terminal notification: %v", notifyErr)
		}
	}
	return strings.TrimSpace(finalText) != "" && finalText != progressStatusOnlyComplete
}

func deliverFinishStream(
	ctx context.Context,
	stream platform.Stream,
	finalText string,
	failed bool,
	stopped bool,
	terminalCardContent string,
) error {
	resolveContent := func(fallback string) string {
		if terminalCardContent != "" {
			return terminalCardContent
		}
		if finalText == progressStatusOnlyComplete {
			return ""
		}
		return firstNonBlank(finalText, fallback)
	}
	switch {
	case stopped:
		content := resolveContent("任务已按请求停止。")
		if stoppable, ok := stream.(platform.StoppableStream); ok {
			return stoppable.Stop(ctx, content)
		}
		return stream.Complete(ctx, content)
	case failed:
		return stream.Fail(ctx, resolveContent("任务执行失败。"))
	default:
		return stream.Complete(ctx, resolveContent(progressDefaultCompletion))
	}
}

func (s *progressSession) discardActiveStreamRecoveryLocked() {
	if s == nil || s.handler == nil {
		return
	}
	reservationID := s.activeRecoveryReservation()
	if reservationID == "" {
		return
	}
	outbox := s.handler.currentTerminalOutbox()
	if outbox == nil {
		return
	}
	if err := outbox.discardReservation(reservationID); err != nil {
		log.Printf("[terminal-outbox] failed to clear completed progress card recovery: %v", err)
		return
	}
	s.stateMu.Lock()
	if s.recoveryReservation == reservationID {
		s.recoveryReservation = ""
	}
	s.stateMu.Unlock()
}
