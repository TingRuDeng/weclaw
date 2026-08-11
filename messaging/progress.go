package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const (
	progressModeOff     = "off"
	progressModeTyping  = "typing"
	progressModeSummary = "summary"
	progressModeVerbose = "verbose"
	progressModeStream  = "stream"
	progressModeDebug   = "debug"

	progressDefaultCompletion  = "任务已完成，正在发送最终结果。"
	progressStatusOnlyComplete = "\x00weclaw_status_only_complete"
)

var progressStageHints = []struct {
	after time.Duration
	text  string
}{
	{after: 20 * time.Second, text: "进展：仍在执行中，连接正常。"},
	{after: 60 * time.Second, text: "进展：任务耗时较长，可能正在读取代码、执行命令或运行验证。"},
	{after: 120 * time.Second, text: "进展：仍在持续执行，请稍等最终结果。"},
}

type progressSendState struct {
	lastSentSummary string
	lastSentAt      time.Time
	sentCount       int
	sawDelta        bool
	sentDeltaNotice bool
	latestSnapshot  progressCardSnapshot
}

type progressCardSnapshot struct {
	summary            string
	text               string
	withPrefix         bool
	structured         bool
	effectiveProgress  bool
	currentExplanation string
	timelineItems      []agent.ProgressEvent
}

type progressReanchorRequest struct {
	Context      context.Context
	Reply        platform.Replier
	Snapshot     progressCardSnapshot
	Title        string
	TransitionID string
	Notice       string
}

type progressReanchorResult struct {
	Moved            bool
	SupersedePending bool
}

type progressSession struct {
	handler               *Handler
	ctx                   context.Context
	cancel                context.CancelFunc
	reply                 platform.Replier
	stream                platform.Stream
	prefix                string
	agentName             string
	workspaceRoot         string
	taskText              string
	cfg                   config.ProgressConfig
	deltaCh               chan string
	snapshotCh            chan progressCardSnapshot
	wg                    sync.WaitGroup
	streamMu              sync.Mutex
	streamOpenAttempted   bool
	lastContent           string
	finished              bool
	terminalClaimed       bool
	detachClaimed         bool
	effectiveProgressSeen bool
	typingStarted         bool
	recoveryReservation   string
	segmentAnchor         *agent.ProgressEvent
	segmentNumber         int
	latestTaskSnapshot    progressCardSnapshot
}

// claimDetachWithoutTerminal 在线性化点冻结终态竞争；实际卡片更新和持久化在锁外完成。
func (s *progressSession) claimDetachWithoutTerminal() bool {
	if s == nil {
		return true
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.finished || s.terminalClaimed {
		return false
	}
	s.finished = true
	s.terminalClaimed = true
	s.detachClaimed = true
	return true
}

// rollbackDetachWithoutTerminalClaim 仅回滚尚未执行实际卡片冻结的 release claim。
// 调用方持有任务投递栅栏，因而不会与合法终态 claim 并发。
func (s *progressSession) rollbackDetachWithoutTerminalClaim() {
	if s == nil {
		return
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if !s.detachClaimed {
		return
	}
	s.detachClaimed = false
	s.finished = false
	s.terminalClaimed = false
}

func (s *progressSession) usesNativeProgressCard() bool {
	return s != nil && s.reply != nil && s.reply.Capabilities().Streaming && progressModeAllowsProgress(s.cfg.Mode)
}

const progressSupersededNotice = "已在新位置继续展示；后续结构化进展将更新到新卡片，最终结果会另发独立结果卡片。"

// startProgressSession 启动平台进度会话，保持旧语义：最终回复由调用方单独发送。
func (h *Handler) startProgressSession(ctx context.Context, reply platform.Replier, prefix string, taskText string, cfg config.ProgressConfig) (func(string), func()) {
	onProgress, finish := h.startProgressSessionWithFinal(ctx, reply, prefix, taskText, cfg)
	return onProgress, func() {
		_ = finish(progressDefaultCompletion, false)
	}
}

// startProgressSessionWithFinal 启动进度会话；各平台能力决定终态只收敛卡片状态还是同时承载最终结果。
func (h *Handler) startProgressSessionWithFinal(ctx context.Context, reply platform.Replier, prefix string, taskText string, cfg config.ProgressConfig) (func(string), func(string, bool) bool) {
	return h.startProgressSessionForAgentWithFinal(ctx, reply, prefix, "", taskText, cfg)
}

// startProgressSessionForAgentWithFinal 为任务卡标题补充 Agent 来源，正文和最终结果保持原样。
func (h *Handler) startProgressSessionForAgentWithFinal(ctx context.Context, reply platform.Replier, prefix string, agentName string, taskText string, cfg config.ProgressConfig) (func(string), func(string, bool) bool) {
	return h.startProgressSessionForWorkspaceAgentWithFinal(ctx, reply, prefix, agentName, "", taskText, cfg)
}

// startProgressSessionForWorkspaceAgentWithFinal 使用任务启动时的工作空间快照生成稳定标题。
func (h *Handler) startProgressSessionForWorkspaceAgentWithFinal(ctx context.Context, reply platform.Replier, prefix string, agentName string, workspaceRoot string, taskText string, cfg config.ProgressConfig) (func(string), func(string, bool) bool) {
	onProgress, finish, _ := h.startProgressSessionForWorkspaceAgentWithHandle(
		ctx, reply, prefix, agentName, workspaceRoot, taskText, cfg,
	)
	return onProgress, finish
}

// startProgressSessionForWorkspaceAgentWithHandle 额外返回内部会话，供终态 outbox 在网络写入前导出 checkpoint。
func (h *Handler) startProgressSessionForWorkspaceAgentWithHandle(ctx context.Context, reply platform.Replier, prefix string, agentName string, workspaceRoot string, taskText string, cfg config.ProgressConfig) (func(string), func(string, bool) bool, *progressSession) {
	if cfg.Mode == "" {
		cfg = config.DefaultProgressConfig()
	}
	if cfg.Mode == progressModeOff {
		return func(string) {}, func(string, bool) bool { return false }, nil
	}

	progressCtx, cancel := context.WithCancel(ctx)
	session := &progressSession{
		handler: h, ctx: progressCtx, cancel: cancel, reply: reply,
		prefix: prefix, agentName: agentName, workspaceRoot: workspaceRoot,
		taskText: taskText, cfg: cfg, deltaCh: make(chan string, 256),
		snapshotCh: make(chan progressCardSnapshot, 1),
	}
	session.start()
	return session.onProgress, session.stopWithFinal, session
}

func (s *progressSession) start() {
	if boolValue(s.cfg.SendAcceptance) {
		title := progressTaskTitleForAgentWorkspace(s.agentName, s.workspaceRoot, s.taskText, 60)
		s.sendText(renderAcceptance(title))
	}
	usesNativeProgress := progressModeAllowsProgress(s.cfg.Mode) && s.reply.Capabilities().Streaming
	if usesNativeProgress && s.ensureStream() == nil {
		s.sendText(renderCardCreationFallback())
	}
	if boolValue(s.cfg.EnableTyping) && !usesNativeProgress {
		s.typingStarted = true
		s.wg.Add(1)
		go s.runTyping()
	}
	if progressModeAllowsProgress(s.cfg.Mode) {
		s.wg.Add(1)
		go s.runProgressLoop()
	}
}

func (s *progressSession) onProgress(delta string) {
	if strings.TrimSpace(delta) == "" {
		return
	}
	if s.cfg.Mode == progressModeStream {
		if summary := renderDeltaProgress(delta, s.cfg); strings.TrimSpace(summary) != "" {
			s.observeEffectiveProgress(true)
			s.rememberLatestTaskSnapshot(progressCardSnapshot{
				text: summary, withPrefix: true, effectiveProgress: true,
			})
		}
	}
	if s.cfg.InitialDelaySeconds <= 0 {
		s.ensureStream()
	}
	select {
	case s.deltaCh <- delta:
	case <-s.ctx.Done():
	default:
	}
}

func (s *progressSession) onTaskProgress(update taskProgressUpdate) {
	if s.cfg.Mode != progressModeStream || !s.reply.Capabilities().Streaming {
		if update.explanation || update.commentary {
			return
		}
		s.onProgress(update.latest)
		return
	}
	if !update.timeline && !update.explanation {
		s.onProgress(update.latest)
		return
	}
	if s.cfg.InitialDelaySeconds <= 0 {
		s.ensureStream()
	}
	snapshot := update.card
	if strings.TrimSpace(snapshot) == "" {
		snapshot = update.latest
	}
	snapshotState := progressCardSnapshot{
		summary: update.latest, text: snapshot, withPrefix: true, structured: update.timeline,
		effectiveProgress:  s.observeEffectiveProgress(taskProgressUpdateHasEffectiveProgress(update)),
		currentExplanation: update.currentExplanation,
		timelineItems:      append([]agent.ProgressEvent(nil), update.timelineItems...),
	}
	s.rememberLatestTaskSnapshot(snapshotState)
	if !snapshotState.effectiveProgress {
		return
	}
	offerLatestProgressSnapshot(s.snapshotCh, snapshotState)
}

func (s *progressSession) observeEffectiveProgress(observed bool) bool {
	s.streamMu.Lock()
	if observed {
		s.effectiveProgressSeen = true
	}
	replied := s.effectiveProgressSeen
	s.streamMu.Unlock()
	return replied
}

func (s *progressSession) rememberLatestTaskSnapshot(snapshot progressCardSnapshot) {
	s.streamMu.Lock()
	snapshot.timelineItems = append([]agent.ProgressEvent(nil), snapshot.timelineItems...)
	s.latestTaskSnapshot = snapshot
	s.streamMu.Unlock()
}

func offerLatestProgressSnapshot(ch chan progressCardSnapshot, snapshot progressCardSnapshot) {
	snapshot.text = strings.TrimSpace(snapshot.text)
	if snapshot.text == "" {
		return
	}
	snapshot.timelineItems = append([]agent.ProgressEvent(nil), snapshot.timelineItems...)
	select {
	case ch <- snapshot:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- snapshot:
	default:
	}
}

func (s *progressSession) stopWithFinal(finalText string, failed bool) bool {
	return s.stopWithTerminal(finalText, failed, false)
}

func (s *progressSession) stopWithTerminal(finalText string, failed bool, stopped bool) bool {
	parentCanceled := s.stopBackground()
	return s.finishStream(parentCanceled, finalText, failed, stopped)
}

func (s *progressSession) stopBackground() bool {
	parentCanceled := s.ctx.Err() != nil
	s.cancel()
	s.wg.Wait()
	if s.typingStarted {
		s.cancelTyping()
	}
	return parentCanceled
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
	s.streamMu.Lock()
	if s.detachClaimed {
		s.detachClaimed = false
	} else if s.finished || s.terminalClaimed {
		s.streamMu.Unlock()
		return nil
	}
	stream := s.stream
	var supersedable platform.SupersedableStream
	var detachable platform.DetachableStream
	if stream != nil {
		supersedable, _ = stream.(platform.SupersedableStream)
		detachable, _ = stream.(platform.DetachableStream)
		if supersedable == nil && detachable == nil {
			s.streamMu.Unlock()
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
	if stream != nil && s.handler != nil && s.recoveryReservation != "" {
		outbox := s.handler.currentTerminalOutbox()
		reporter, routeOK := optionalDeliveryRouteReporter(s.reply)
		exporter, exportOK := stream.(platform.DurableStreamReferenceExporter)
		detachPreparer, detachPrepareOK := optionalDurableStreamDetachPreparer(s.reply)
		supersedePreparer, supersedePrepareOK := optionalDurableStreamSupersedePreparer(s.reply)
		prepared, deliverOK := stream.(platform.PreparedSupersedableStream)
		prepareOK := detachPrepareOK || supersedePrepareOK
		if outbox != nil && routeOK && reporter.DeliveryRoute().Valid() && exportOK && prepareOK && deliverOK {
			reference, err := exporter.DurableReference()
			if err != nil {
				s.streamMu.Unlock()
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
				s.streamMu.Unlock()
				return fmt.Errorf("prepare released progress card freeze: %w", err)
			}
			if err := outbox.beginStreamReanchor(s.recoveryReservation); err != nil {
				s.streamMu.Unlock()
				return fmt.Errorf("reserve released progress card freeze: %w", err)
			}
			pending := pendingStreamSupersede{
				ID: transitionID, Route: reporter.DeliveryRoute(), Checkpoint: checkpoint,
			}
			if err := outbox.detachStreamReservation(s.recoveryReservation, pending); err != nil {
				outbox.endStreamReanchor(s.recoveryReservation)
				s.streamMu.Unlock()
				return fmt.Errorf("persist released progress card freeze: %w", err)
			}
			durable = &durableDetach{
				outbox: outbox, reservation: s.recoveryReservation,
				transitionID: transitionID, checkpoint: checkpoint, prepared: prepared,
			}
			s.recoveryReservation = ""
		}
	}
	s.finished = true
	s.terminalClaimed = true
	s.streamMu.Unlock()

	s.stopBackground()
	if supersedable == nil && detachable == nil {
		s.streamMu.Lock()
		s.discardActiveStreamRecoveryLocked()
		s.streamMu.Unlock()
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
	s.streamMu.Lock()
	s.discardActiveStreamRecoveryLocked()
	s.streamMu.Unlock()
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
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
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
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
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
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.terminalResultPresentationLocked()
}

func (s *progressSession) terminalResultPresentationLocked() (string, bool) {
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
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	stream := s.stream
	if stream == nil || !s.reply.Capabilities().Streaming {
		return preparedProgressTerminal{}, nil
	}
	durable, ok := stream.(platform.DurableTerminalStream)
	if !ok {
		return preparedProgressTerminal{}, platform.ErrUnsupported
	}
	currentReply := s.reply
	if currentReply == nil {
		currentReply = replyWriter
	}
	content, terminalFailed, consumed := progressTerminalArguments(currentReply, parentCanceled, finalText, failed, stopped)
	if strings.TrimSpace(content) == "" {
		content = s.latestTaskSnapshotContentLocked()
	}
	s.terminalClaimed = true
	s.finished = true
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
	prepared := preparedProgressTerminal{
		checkpoint:   &checkpoint,
		consumed:     consumed,
		notification: renderStreamTerminalNotification(parentCanceled, failed, stopped, finalText),
		reply:        currentReply,
	}
	return prepared, nil
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

func (s *progressSession) runTyping() {
	defer s.wg.Done()
	s.sendTyping()
	ticker := time.NewTicker(durationSeconds(s.cfg.TypingHeartbeatSeconds, 8*time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.sendTyping()
		}
	}
}

func (s *progressSession) runProgressLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(progressTickerInterval(s.cfg))
	defer ticker.Stop()

	startedAt := time.Now()
	state := progressSendState{}
	stageIndex := 0
	tail := ""
	for {
		select {
		case <-s.ctx.Done():
			return
		case delta := <-s.deltaCh:
			tail = s.handleProgressDelta(delta, tail, startedAt, &state)
		case snapshot := <-s.snapshotCh:
			s.handleProgressSnapshot(snapshot, startedAt, &state)
		case now := <-ticker.C:
			s.handleTimedProgress(now, startedAt, &stageIndex, &state)
		}
	}
}

func (s *progressSession) handleProgressSnapshot(snapshot progressCardSnapshot, startedAt time.Time, state *progressSendState) {
	snapshot.text = strings.TrimSpace(snapshot.text)
	if snapshot.text == "" {
		return
	}
	state.sawDelta = true
	state.latestSnapshot = snapshot
	if time.Since(startedAt) < durationSeconds(s.cfg.InitialDelaySeconds, 0) {
		return
	}
	s.sendSnapshotIfAllowed(snapshot, state)
}

func (s *progressSession) handleProgressDelta(delta string, tail string, startedAt time.Time, state *progressSendState) string {
	if strings.TrimSpace(delta) == "" {
		return tail
	}
	state.sawDelta = true
	tail = truncateTailRunes(tail+delta, s.cfg.MaxTailRunes)
	if time.Since(startedAt) < durationSeconds(s.cfg.InitialDelaySeconds, 0) {
		return tail
	}
	summary := renderDeltaProgress(delta, s.cfg)
	if state.sentDeltaNotice && s.cfg.Mode != progressModeStream {
		return tail
	}
	s.sendProgressIfAllowed(summary, state)
	state.sentDeltaNotice = true
	return tail
}

func (s *progressSession) handleTimedProgress(now time.Time, startedAt time.Time, stageIndex *int, state *progressSendState) {
	elapsed := now.Sub(startedAt)
	if elapsed < durationSeconds(s.cfg.InitialDelaySeconds, 0) {
		return
	}
	// stream 已经在任务开始时展示统一活跃提示；定时心跳不得用“等待 Agent”等合成文案覆盖它。
	if s.cfg.Mode == progressModeStream {
		return
	}
	if state.latestSnapshot.text != "" {
		if state.sentCount == 0 {
			s.sendSnapshotIfAllowed(state.latestSnapshot, state)
		}
		return
	}
	if state.sentCount == 0 && !state.sawDelta {
		s.sendProgressIfAllowed(renderInitialProgress(), state)
		return
	}
	for *stageIndex < len(progressStageHints) && elapsed >= progressStageHints[*stageIndex].after {
		s.sendProgressIfAllowed(progressStageHints[*stageIndex].text, state)
		*stageIndex = *stageIndex + 1
	}
}

func (s *progressSession) sendProgressIfAllowed(summary string, state *progressSendState) {
	s.sendContentIfAllowed(summary, state, true)
}

func (s *progressSession) sendSnapshotIfAllowed(snapshot progressCardSnapshot, state *progressSendState) {
	now := time.Now()
	if !shouldSendProgress(now, *state, snapshot.text, s.cfg) {
		return
	}
	if !s.sendSnapshotContent(snapshot) {
		return
	}
	state.lastSentSummary = snapshot.text
	state.lastSentAt = now
	state.sentCount++
}

func (s *progressSession) sendContentIfAllowed(summary string, state *progressSendState, withPrefix bool) {
	now := time.Now()
	if !shouldSendProgress(now, *state, summary, s.cfg) {
		return
	}
	if !s.sendContent(summary, withPrefix) {
		return
	}
	state.lastSentSummary = summary
	state.lastSentAt = now
	state.sentCount++
}

func (s *progressSession) send(text string) bool {
	return s.sendContent(text, true)
}

func (s *progressSession) sendContent(text string, withPrefix bool) bool {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.finished || s.terminalClaimed {
		return false
	}
	stream := s.ensureStreamLocked()
	if stream != nil {
		content := text
		if s.cfg.Mode == progressModeStream {
			content = appendActiveThinkingIndicator(content)
		}
		if withPrefix {
			content = s.prefix + content
		}
		if err := stream.Update(s.ctx, content); err != nil {
			log.Printf("[handler] failed to update progress stream: %v", err)
			return false
		}
		s.lastContent = content
		if err := s.persistActiveStreamRecoveryLocked(stream, s.reply); err != nil {
			log.Printf("[terminal-outbox] failed to refresh active progress card recovery: %v", err)
		}
		return true
	}
	if s.reply.Capabilities().Streaming {
		return false
	}
	return s.sendText(text)
}

func (s *progressSession) sendSnapshotContent(snapshot progressCardSnapshot) bool {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.finished || s.terminalClaimed {
		return false
	}
	stream := s.ensureStreamLocked()
	if stream == nil {
		if s.reply.Capabilities().Streaming {
			return false
		}
		return s.sendText(snapshot.text)
	}
	content := s.activeSnapshotContentLocked(snapshot)
	presentation := s.snapshotPresentationLocked(snapshot)
	if snapshot.withPrefix {
		content = s.prefix + content
		presentation.Details = s.prefix + presentation.Details
	}
	if preflighter, ok := stream.(platform.StreamPresentationPreflighter); ok {
		if err := preflighter.PreflightPresentation(presentation); err != nil {
			if !errors.Is(err, platform.ErrStreamContentTooLarge) || !snapshot.structured || len(snapshot.timelineItems) == 0 {
				log.Printf("[handler] failed to preflight progress presentation: %v", err)
				return false
			}
			continued, continueErr := s.continueProgressStreamLocked(snapshot)
			if continueErr != nil {
				return false
			}
			return continued
		}
	}
	if preflighter, ok := stream.(platform.StreamContentPreflighter); ok {
		if err := preflighter.PreflightUpdate(content); err != nil {
			if !errors.Is(err, platform.ErrStreamContentTooLarge) || !snapshot.structured || len(snapshot.timelineItems) == 0 {
				log.Printf("[handler] failed to preflight progress stream: %v", err)
				return false
			}
			continued, continueErr := s.continueProgressStreamLocked(snapshot)
			if continueErr != nil {
				log.Printf("[handler] failed to continue oversized progress stream: %v", continueErr)
				return false
			}
			return continued
		}
	}
	if structured, ok := stream.(platform.StructuredProgressStream); ok {
		if err := structured.UpdatePresentation(s.ctx, presentation); err != nil {
			log.Printf("[handler] failed to update structured progress stream: %v", err)
			return false
		}
	} else if err := stream.Update(s.ctx, content); err != nil {
		log.Printf("[handler] failed to update progress stream: %v", err)
		return false
	}
	s.lastContent = content
	if err := s.persistActiveStreamRecoveryLocked(stream, s.reply); err != nil {
		log.Printf("[terminal-outbox] failed to refresh active progress card recovery: %v", err)
	}
	return true
}

func (s *progressSession) snapshotPresentationLocked(snapshot progressCardSnapshot) platform.StreamPresentation {
	summary := strings.TrimSpace(snapshot.summary)
	if summary == "" {
		summary = snapshot.text
	}
	return platform.StreamPresentation{Summary: summary, Details: s.activeSnapshotContentLocked(snapshot)}
}

func (s *progressSession) activeSnapshotContentLocked(snapshot progressCardSnapshot) string {
	return appendActiveThinkingIndicator(s.segmentedSnapshotContentLocked(snapshot))
}

func (s *progressSession) segmentedSnapshotContentLocked(snapshot progressCardSnapshot) string {
	if !snapshot.structured || len(snapshot.timelineItems) == 0 {
		return snapshot.text
	}
	start := 0
	if s.segmentAnchor != nil {
		if index := matchingTaskProgressEntry(snapshot.timelineItems, *s.segmentAnchor); index >= 0 {
			start = index
		}
	}
	content, timeline := renderTaskProgressTimeline(snapshot.timelineItems[start:], snapshot.text)
	if !timeline {
		return snapshot.text
	}
	return appendTaskCurrentExplanation(content, snapshot.currentExplanation)
}

func (s *progressSession) latestTaskSnapshotContentLocked() string {
	snapshot := s.latestTaskSnapshot
	if strings.TrimSpace(snapshot.text) == "" {
		return ""
	}
	content := s.segmentedSnapshotContentLocked(snapshot)
	if snapshot.withPrefix {
		content = s.prefix + content
	}
	return strings.TrimSpace(content)
}

func (s *progressSession) continueProgressStreamLocked(snapshot progressCardSnapshot) (bool, error) {
	if _, ok := s.stream.(platform.SupersedableStream); !ok {
		return false, fmt.Errorf("oversized progress stream does not support continuation")
	}
	segmentStart := len(snapshot.timelineItems) - 1
	nextSegment := s.segmentNumber + 1
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
		Reply:        s.reply,
		Snapshot:     segmentSnapshot,
		Title:        fmt.Sprintf("%s · 进度 %d", baseTitle, nextSegment),
		TransitionID: uuid.NewString(),
		Notice:       fmt.Sprintf("%s\n\n---\n后续进度见第 %d 张卡片。", oldContent, nextSegment),
	})
	if !result.Moved {
		return false, err
	}
	segmentAnchor := snapshot.timelineItems[segmentStart]
	s.segmentAnchor = &segmentAnchor
	s.segmentNumber = nextSegment
	if err != nil {
		log.Printf("[handler] continued progress card but failed to freeze previous segment: %v", err)
	}
	if result.SupersedePending {
		log.Printf("[handler] continued progress card; previous segment supersede is queued for retry")
	}
	return true, nil
}

func (s *progressSession) sendText(text string) bool {
	if err := s.reply.SendText(s.ctx, s.prefix+text); err != nil {
		log.Printf("[handler] failed to send progress message: %v", err)
		return false
	}
	return true
}

func (s *progressSession) ensureStream() platform.Stream {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.ensureStreamLocked()
}

func (s *progressSession) ensureStreamLocked() platform.Stream {
	if s.stream != nil || s.streamOpenAttempted || !progressModeAllowsProgress(s.cfg.Mode) {
		return s.stream
	}
	s.streamOpenAttempted = true
	stream, err := s.reply.OpenStream(s.ctx, platform.StreamOptions{
		Title: progressTaskTitleForAgentWorkspace(s.agentName, s.workspaceRoot, s.taskText, 60), InitialContent: renderInitialCardProgress(),
	})
	if err != nil {
		log.Printf("[handler] failed to open progress stream: %v", err)
		return nil
	}
	s.stream = stream
	s.lastContent = renderInitialCardProgress()
	s.segmentNumber = 1
	if err := s.persistActiveStreamRecoveryLocked(stream, s.reply); err != nil {
		log.Printf("[terminal-outbox] failed to persist active progress card recovery: %v", err)
	}
	s.registerStreamRecoveryChangeHandlerLocked(stream)
	return stream
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
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.finished || s.terminalClaimed || s.stream == nil {
		return
	}
	if err := s.persistActiveStreamRecoveryLocked(s.stream, s.reply); err != nil {
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
	if s.recoveryReservation != "" {
		return outbox.refreshStreamReservation(s.recoveryReservation, reporter.DeliveryRoute(), reference)
	}
	trace, _ := observability.TraceFromContext(s.ctx)
	resultTitle, richResult := s.terminalResultPresentationLocked()
	reservation, err := outbox.reserve(terminalOutboxDraft{
		Route: reporter.DeliveryRoute(), AgentName: s.agentName, Stopped: true,
		Stream: &reference, ResultTitle: resultTitle, RichResult: richResult,
		Text: activeStreamRestartText, Trace: trace, ActiveStreamRecovery: true,
	})
	if err != nil {
		return err
	}
	s.recoveryReservation = reservation.ID
	return nil
}

func (s *progressSession) activeRecoveryReservation() string {
	if s == nil {
		return ""
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.recoveryReservation
}

func (s *progressSession) refreshActiveStreamRecoveryTrace(trace observability.TraceContext) error {
	if s == nil || s.handler == nil {
		return nil
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.recoveryReservation == "" {
		return nil
	}
	outbox := s.handler.currentTerminalOutbox()
	if outbox == nil {
		return nil
	}
	if err := outbox.refreshStreamReservationTrace(s.recoveryReservation, trace); err != nil {
		return err
	}
	return nil
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
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.reanchorStreamLocked(progressReanchorRequest{
		Context:      moveCtx,
		Reply:        reply,
		Snapshot:     snapshot,
		Title:        progressTaskTitleForAgentWorkspace(s.agentName, s.workspaceRoot, s.taskText, 60),
		TransitionID: transitionID,
		Notice:       progressSupersededNotice,
	})
}

// reanchorStreamLocked 必须在持有 streamMu 时调用。只有 durable 事务落盘成功后，
// 才会改变 task-card binding 和内存中的权威 stream。
func (s *progressSession) reanchorStreamLocked(request progressReanchorRequest) (progressReanchorResult, error) {
	if s.finished || s.terminalClaimed || s.stream == nil {
		return progressReanchorResult{}, nil
	}
	oldStream, ok := s.stream.(platform.SupersedableStream)
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

	initialContent := renderInitialCardProgress()
	var initialPresentation *platform.StreamPresentation
	withPrefix := request.Snapshot.withPrefix
	if request.Snapshot.effectiveProgress || request.Snapshot.structured {
		snapshot := request.Snapshot
		if strings.TrimSpace(snapshot.text) == "" {
			snapshot.text = snapshot.summary
		}
		initialContent = s.activeSnapshotContentLocked(snapshot)
		p := s.snapshotPresentationLocked(snapshot)
		initialPresentation = &p
	} else if s.latestTaskSnapshot.effectiveProgress {
		withPrefix = s.latestTaskSnapshot.withPrefix
		initialContent = s.activeSnapshotContentLocked(s.latestTaskSnapshot)
		p := s.snapshotPresentationLocked(s.latestTaskSnapshot)
		initialPresentation = &p
	} else if strings.TrimSpace(s.latestTaskSnapshot.text) == "" && strings.TrimSpace(request.Snapshot.summary) != "" {
		initialContent = appendActiveThinkingIndicator(request.Snapshot.summary)
	} else if strings.TrimSpace(s.latestTaskSnapshot.text) == "" && trimActiveThinkingIndicator(s.lastContent) != "" {
		initialContent = appendActiveThinkingIndicator(trimActiveThinkingIndicator(s.lastContent))
	}
	if withPrefix {
		initialContent = s.prefix + initialContent
		if initialPresentation != nil {
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
	oldRouteReporter, oldRouteOK := optionalDeliveryRouteReporter(s.reply)
	newRouteReporter, newRouteOK := optionalDeliveryRouteReporter(reply)
	oldExporter, oldExportOK := s.stream.(platform.DurableStreamReferenceExporter)
	preparer, prepareOK := optionalDurableStreamSupersedePreparer(s.reply)
	preparedStream, preparedOK := s.stream.(platform.PreparedSupersedableStream)
	if outbox != nil && s.recoveryReservation != "" && oldRouteOK && newRouteOK && oldExportOK && prepareOK && preparedOK {
		oldRoute := oldRouteReporter.DeliveryRoute()
		newRoute := newRouteReporter.DeliveryRoute()
		if oldRoute.Valid() && newRoute.Valid() {
			transitionID := strings.TrimSpace(request.TransitionID)
			if _, err := uuid.Parse(transitionID); err != nil {
				return progressReanchorResult{}, fmt.Errorf("invalid progress reanchor transition ID")
			}
			if err := outbox.beginStreamReanchor(s.recoveryReservation); err != nil {
				return progressReanchorResult{}, fmt.Errorf("reserve progress reanchor delivery: %w", err)
			}
			defer outbox.endStreamReanchor(s.recoveryReservation)
			oldReference, err := oldExporter.DurableReference()
			if err != nil {
				return progressReanchorResult{}, fmt.Errorf("export previous progress card: %w", err)
			}
			checkpoint, err := preparer.PrepareSupersedeFromReference(oldReference, notice, transitionID)
			if err != nil {
				return progressReanchorResult{}, fmt.Errorf("prepare previous progress card supersede: %w", err)
			}
			durable = &durableReanchor{
				outbox: outbox, reservation: s.recoveryReservation,
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
	rebindProgressTaskCard(s.reply, reply)
	s.stream = stream
	s.reply = reply
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

func progressReplier(reply platform.Replier) platform.Replier {
	for depth := 0; depth < 4 && reply != nil; depth++ {
		provider, ok := reply.(platform.ProgressReplierProvider)
		if !ok {
			return reply
		}
		next := provider.ProgressReplier()
		if next == nil {
			return reply
		}
		reply = next
	}
	return reply
}

func (s *progressSession) currentTerminalReply() platform.Replier {
	if s == nil {
		return nil
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.reply
}

func progressTaskTitleForAgentWorkspace(agentName string, workspaceRoot string, taskText string, maxRunes int) string {
	if strings.TrimSpace(agentName) == "" {
		return progressTaskTitle(taskText, maxRunes)
	}
	title := agentDisplayName(agentName)
	if workspace := progressWorkspaceName(workspaceRoot); workspace != "" {
		title += " · " + workspace
	}
	return progressTaskTitle(title, maxRunes)
}

func progressResultTitleForAgentWorkspace(agentName string, workspaceRoot string, maxRunes int) string {
	title := "WeClaw"
	if strings.TrimSpace(agentName) != "" {
		title = agentDisplayName(agentName)
	}
	if workspace := progressWorkspaceName(workspaceRoot); workspace != "" {
		title += " · " + workspace
	}
	return progressTaskTitle(title, maxRunes)
}

func progressWorkspaceName(workspaceRoot string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return ""
	}
	name := filepath.Base(filepath.Clean(workspaceRoot))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

func (s *progressSession) sendTyping() {
	if err := s.reply.Typing(s.ctx, true); err != nil {
		log.Printf("[handler] failed to send typing state: %v", err)
	}
}

func (s *progressSession) cancelTyping() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.reply.Typing(ctx, false); err != nil {
		log.Printf("[handler] failed to send typing cancel: %v", err)
	}
}

func (s *progressSession) finishStream(parentCanceled bool, finalText string, failed bool, stopped bool) bool {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.finished || s.terminalClaimed {
		return false
	}
	stream := s.stream
	if stream == nil || !s.reply.Capabilities().Streaming {
		return false
	}
	s.terminalClaimed = true
	s.finished = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped = stopped || parentCanceled
	terminalCardContent := ""
	if s.reply.Capabilities().FinalReplyOutsideStream {
		terminalCardContent = s.latestTaskSnapshotContentLocked()
	}
	var err error
	switch {
	case stopped:
		content := firstNonBlank(finalText, "任务已按请求停止。")
		if finalText == progressStatusOnlyComplete {
			content = ""
		}
		if terminalCardContent != "" {
			content = terminalCardContent
		}
		if stoppable, ok := stream.(platform.StoppableStream); ok {
			err = stoppable.Stop(ctx, content)
		} else {
			err = stream.Complete(ctx, content)
		}
	case failed:
		content := firstNonBlank(finalText, "任务执行失败。")
		if finalText == progressStatusOnlyComplete {
			content = ""
		}
		if terminalCardContent != "" {
			content = terminalCardContent
		}
		err = stream.Fail(ctx, content)
	case finalText == progressStatusOnlyComplete:
		err = stream.Complete(ctx, terminalCardContent)
	case strings.TrimSpace(finalText) != "":
		content := finalText
		if terminalCardContent != "" {
			content = terminalCardContent
		}
		err = stream.Complete(ctx, content)
	default:
		content := progressDefaultCompletion
		if terminalCardContent != "" {
			content = terminalCardContent
		}
		err = stream.Complete(ctx, content)
	}
	if err != nil {
		log.Printf("[handler] failed to finish progress stream: %v", err)
		return false
	}
	s.discardActiveStreamRecoveryLocked()
	notification := renderStreamTerminalNotification(parentCanceled, failed, stopped, finalText)
	if notification != "" && s.reply.Capabilities().StreamCompletionNotification {
		if notifyErr := s.reply.SendText(ctx, notification); notifyErr != nil {
			log.Printf("[handler] failed to send stream terminal notification: %v", notifyErr)
		}
	}
	return strings.TrimSpace(finalText) != "" && finalText != progressStatusOnlyComplete
}

func (s *progressSession) discardActiveStreamRecoveryLocked() {
	if s == nil || s.handler == nil || s.recoveryReservation == "" {
		return
	}
	outbox := s.handler.currentTerminalOutbox()
	if outbox == nil {
		return
	}
	if err := outbox.discardReservation(s.recoveryReservation); err != nil {
		log.Printf("[terminal-outbox] failed to clear completed progress card recovery: %v", err)
		return
	}
	s.recoveryReservation = ""
}
