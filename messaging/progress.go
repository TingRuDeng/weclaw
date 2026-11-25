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
	"github.com/fastclaw-ai/weclaw/platform"
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
	progressSupersededNotice   = "已在新位置继续展示；后续结构化进展将更新到新卡片，最终结果会另发独立结果卡片。"
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

// progressSession 维护单个任务的进度推送与流式卡片状态。
// 锁分层规则（严格禁止反向加锁）：
//   - stateMu：极轻纯内存状态锁，严禁持有该锁做网络或磁盘 I/O；供持有 tasks.mu/task.mu 的路径无阻塞访问。
//   - ioMu：串行化流创建、卡片更新、重锚、终态与 outbox 落盘；持 ioMu 期间可短暂获取 stateMu，严禁反向。
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
	ioMu                  sync.Mutex
	stateMu               sync.Mutex
	streamOpenAttempted   bool
	streamOpenErr         error
	lastContent           string
	finished              bool
	terminalClaimed       bool
	detachClaimed         bool
	effectiveProgressSeen bool
	typingStarted         bool
	recoveryReservation   string
	deliveryGuard         terminalDeliveryGuard
	segmentAnchor         *agent.ProgressEvent
	segmentNumber         int
	latestTaskSnapshot    progressCardSnapshot
}

func (s *progressSession) isClosedOrClaimed() bool {
	s.stateMu.Lock()
	closed := s.finished || s.terminalClaimed
	s.stateMu.Unlock()
	return closed
}

// setDeliveryGuardOnly 仅更新内存中的投递守卫，供持 task.mu 的附着路径无锁竞态、无 I/O 调用。
func (s *progressSession) setDeliveryGuardOnly(guard terminalDeliveryGuard) {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.deliveryGuard = guard
	s.stateMu.Unlock()
}

func (s *progressSession) setTerminalDeliveryGuard(guard terminalDeliveryGuard) {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.deliveryGuard = guard
	reservationID := s.recoveryReservation
	s.stateMu.Unlock()
	if reservationID != "" && s.handler != nil {
		if outbox := s.handler.currentTerminalOutbox(); outbox != nil {
			if err := outbox.refreshReservationDeliveryGuard(reservationID, guard); err != nil {
				log.Printf("[terminal-outbox] failed to refresh delivery guard: %v", err)
			}
		}
	}
}

func (s *progressSession) terminalDeliveryGuardSnapshot() terminalDeliveryGuard {
	if s == nil {
		return terminalDeliveryGuard{}
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.deliveryGuard
}

// claimDetachWithoutTerminal 在线性化点冻结终态竞争；只获取纯内存 stateMu，永不等待网络 I/O。
func (s *progressSession) claimDetachWithoutTerminal() bool {
	if s == nil {
		return true
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
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
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if !s.detachClaimed {
		return
	}
	s.detachClaimed = false
	s.finished = false
	s.terminalClaimed = false
}

func (s *progressSession) usesNativeProgressCard() bool {
	if s == nil {
		return false
	}
	s.stateMu.Lock()
	reply := s.reply
	s.stateMu.Unlock()
	return reply != nil && reply.Capabilities().Streaming && progressModeAllowsProgress(s.cfg.Mode)
}

func (s *progressSession) nativeProgressReadyError() error {
	if s == nil || !s.usesNativeProgressCard() {
		return nil
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.stream != nil {
		return nil
	}
	if s.streamOpenErr != nil {
		return s.streamOpenErr
	}
	return fmt.Errorf("任务进度卡尚未创建")
}

// startProgressSessionForWorkspaceAgentWithHandle 额外返回内部会话，供终态 outbox 在网络写入前导出 checkpoint。
func (h *Handler) startProgressSessionForWorkspaceAgentWithHandle(ctx context.Context, reply platform.Replier, prefix string, agentName string, workspaceRoot string, taskText string, cfg config.ProgressConfig) (func(string), func(string, bool) bool, *progressSession) {
	return h.startProgressSessionForWorkspaceAgentWithGuard(ctx, reply, prefix, agentName, workspaceRoot, taskText, cfg, terminalDeliveryGuard{})
}

func (h *Handler) startProgressSessionForWorkspaceAgentWithGuard(ctx context.Context, reply platform.Replier, prefix string, agentName string, workspaceRoot string, taskText string, cfg config.ProgressConfig, guard terminalDeliveryGuard) (func(string), func(string, bool) bool, *progressSession) {
	return h.startProgressSessionForWorkspaceAgentWithGuardAndSnapshot(
		ctx, reply, prefix, agentName, workspaceRoot, taskText, cfg, guard, progressCardSnapshot{},
	)
}

func (h *Handler) startProgressSessionForWorkspaceAgentWithGuardAndSnapshot(ctx context.Context, reply platform.Replier, prefix string, agentName string, workspaceRoot string, taskText string, cfg config.ProgressConfig, guard terminalDeliveryGuard, initial progressCardSnapshot) (func(string), func(string, bool) bool, *progressSession) {
	if cfg.Mode == "" {
		cfg = config.DefaultProgressConfig()
	}
	if cfg.Mode == progressModeOff {
		return func(string) {}, func(string, bool) bool { return false }, nil
	}
	reply = progressReplier(reply)
	if reply == nil {
		return func(string) {}, func(string, bool) bool { return false }, nil
	}
	cfg = normalizeProgressConfigForCapabilities(cfg, reply.Capabilities())

	progressCtx, cancel := context.WithCancel(ctx)
	session := &progressSession{
		handler: h, ctx: progressCtx, cancel: cancel, reply: reply,
		prefix: prefix, agentName: agentName, workspaceRoot: workspaceRoot,
		taskText: taskText, cfg: cfg, deltaCh: make(chan string, 256),
		snapshotCh:    make(chan progressCardSnapshot, 1),
		deliveryGuard: guard,
		latestTaskSnapshot: progressCardSnapshot{
			summary: initial.summary, text: initial.text, withPrefix: initial.withPrefix,
			structured: initial.structured, effectiveProgress: initial.effectiveProgress,
			currentExplanation: initial.currentExplanation,
			timelineItems:      append([]agent.ProgressEvent(nil), initial.timelineItems...),
		},
		effectiveProgressSeen: initial.effectiveProgress,
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
	reply := s.currentTerminalReply()
	if s.cfg.Mode != progressModeStream || reply == nil || !reply.Capabilities().Streaming {
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
	s.stateMu.Lock()
	if observed {
		s.effectiveProgressSeen = true
	}
	replied := s.effectiveProgressSeen
	s.stateMu.Unlock()
	return replied
}

func (s *progressSession) rememberLatestTaskSnapshot(snapshot progressCardSnapshot) {
	snapshot.timelineItems = append([]agent.ProgressEvent(nil), snapshot.timelineItems...)
	s.stateMu.Lock()
	s.latestTaskSnapshot = snapshot
	s.stateMu.Unlock()
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
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	if s.isClosedOrClaimed() {
		return false
	}
	stream := s.ensureStreamLocked()
	reply := s.currentTerminalReply()
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
		if !s.isClosedOrClaimed() {
			if err := s.persistActiveStreamRecoveryLocked(stream, reply); err != nil {
				log.Printf("[terminal-outbox] failed to refresh active progress card recovery: %v", err)
			}
		}
		return true
	}
	if reply != nil && reply.Capabilities().Streaming {
		return false
	}
	return s.sendText(text)
}

func (s *progressSession) sendSnapshotContent(snapshot progressCardSnapshot) bool {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	if s.isClosedOrClaimed() {
		return false
	}
	stream := s.ensureStreamLocked()
	reply := s.currentTerminalReply()
	if stream == nil {
		if reply != nil && reply.Capabilities().Streaming {
			return false
		}
		return s.sendText(snapshot.text)
	}
	s.stateMu.Lock()
	content := s.activeSnapshotContentStateLocked(snapshot)
	presentation := s.snapshotPresentationStateLocked(snapshot)
	s.stateMu.Unlock()
	if snapshot.withPrefix {
		content = s.prefix + content
		presentation.Preview = s.prefix + presentation.Preview
		presentation.Details = s.prefix + presentation.Details
	}
	if handled, ok := s.preflightAndContinueSnapshotLocked(stream, snapshot, content, presentation); handled {
		return ok
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
	if !s.isClosedOrClaimed() {
		if err := s.persistActiveStreamRecoveryLocked(stream, reply); err != nil {
			log.Printf("[terminal-outbox] failed to refresh active progress card recovery: %v", err)
		}
	}
	return true
}

// preflightAndContinueSnapshotLocked 统一预检卡片字节软上限，预计超限时触发自动续卡。
func (s *progressSession) preflightAndContinueSnapshotLocked(
	stream platform.Stream,
	snapshot progressCardSnapshot,
	content string,
	presentation platform.StreamPresentation,
) (handled bool, ok bool) {
	check := func(kind string, err error) (bool, bool) {
		if err == nil {
			return false, false
		}
		if !errors.Is(err, platform.ErrStreamContentTooLarge) || !snapshot.structured || len(snapshot.timelineItems) == 0 {
			log.Printf("[handler] failed to preflight progress %s: %v", kind, err)
			return true, false
		}
		continued, continueErr := s.continueProgressStreamLocked(snapshot)
		if continueErr != nil {
			if kind == "stream" {
				log.Printf("[handler] failed to continue oversized progress stream: %v", continueErr)
			}
			return true, false
		}
		return true, continued
	}
	if preflighter, ok := stream.(platform.StreamPresentationPreflighter); ok {
		if handled, continued := check("presentation", preflighter.PreflightPresentation(presentation)); handled {
			return true, continued
		}
	}
	if preflighter, ok := stream.(platform.StreamContentPreflighter); ok {
		if handled, continued := check("stream", preflighter.PreflightUpdate(content)); handled {
			return true, continued
		}
	}
	return false, false
}

func (s *progressSession) snapshotPresentationStateLocked(snapshot progressCardSnapshot) platform.StreamPresentation {
	summary := strings.TrimSpace(snapshot.summary)
	if summary == "" {
		summary = snapshot.text
	}
	details := s.activeSnapshotContentStateLocked(snapshot)
	return platform.StreamPresentation{Summary: summary, Preview: details, Details: details}
}

func (s *progressSession) activeSnapshotContentStateLocked(snapshot progressCardSnapshot) string {
	return appendActiveThinkingIndicator(s.segmentedSnapshotContentStateLocked(snapshot))
}

func (s *progressSession) segmentedSnapshotContentStateLocked(snapshot progressCardSnapshot) string {
	if !snapshot.structured || len(snapshot.timelineItems) == 0 {
		return snapshot.text
	}
	items := s.segmentedSnapshotTimelineItemsStateLocked(snapshot)
	content, timeline := renderTaskProgressTimeline(items, snapshot.text)
	if !timeline {
		return snapshot.text
	}
	return appendTaskCurrentExplanation(content, snapshot.currentExplanation)
}

func (s *progressSession) segmentedSnapshotTimelineItemsStateLocked(snapshot progressCardSnapshot) []agent.ProgressEvent {
	start := 0
	if s.segmentAnchor != nil {
		if index := matchingTaskProgressEntry(snapshot.timelineItems, *s.segmentAnchor); index >= 0 {
			start = index
		}
	}
	return snapshot.timelineItems[start:]
}

func (s *progressSession) latestTaskSnapshotContentStateLocked() string {
	snapshot := s.latestTaskSnapshot
	if strings.TrimSpace(snapshot.text) == "" {
		return ""
	}
	content := s.segmentedSnapshotContentStateLocked(snapshot)
	if snapshot.withPrefix {
		content = s.prefix + content
	}
	return strings.TrimSpace(content)
}

func (s *progressSession) sendText(text string) bool {
	reply := s.currentTerminalReply()
	if reply == nil {
		return false
	}
	if err := reply.SendText(s.ctx, s.prefix+text); err != nil {
		log.Printf("[handler] failed to send progress message: %v", err)
		return false
	}
	return true
}

func (s *progressSession) ensureStream() platform.Stream {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	return s.ensureStreamLocked()
}

func (s *progressSession) ensureStreamLocked() platform.Stream {
	s.stateMu.Lock()
	existing := s.stream
	reply := s.reply
	closed := s.finished || s.terminalClaimed
	if existing != nil || s.streamOpenAttempted || closed || !progressModeAllowsProgress(s.cfg.Mode) || reply == nil || !reply.Capabilities().Streaming {
		s.stateMu.Unlock()
		return existing
	}
	initialContent := renderInitialCardProgress()
	var initialPresentation *platform.StreamPresentation
	if s.latestTaskSnapshot.effectiveProgress || s.latestTaskSnapshot.structured {
		snapshot := s.latestTaskSnapshot
		if strings.TrimSpace(snapshot.text) == "" {
			snapshot.text = snapshot.summary
		}
		initialContent = s.activeSnapshotContentStateLocked(snapshot)
		presentation := s.snapshotPresentationStateLocked(snapshot)
		initialPresentation = &presentation
		if snapshot.withPrefix {
			initialContent = s.prefix + initialContent
			initialPresentation.Preview = s.prefix + initialPresentation.Preview
			initialPresentation.Details = s.prefix + initialPresentation.Details
		}
	}
	s.stateMu.Unlock()

	s.streamOpenAttempted = true
	stream, err := reply.OpenStream(s.ctx, platform.StreamOptions{
		Title:          progressTaskTitleForAgentWorkspace(s.agentName, s.workspaceRoot, s.taskText, 60),
		InitialContent: initialContent, InitialPresentation: initialPresentation,
	})
	if err != nil {
		s.stateMu.Lock()
		s.streamOpenErr = err
		s.stateMu.Unlock()
		log.Printf("[handler] failed to open progress stream: %v", err)
		return nil
	}
	s.stateMu.Lock()
	s.stream = stream
	s.segmentNumber = 1
	stillOpen := !s.finished && !s.terminalClaimed
	s.stateMu.Unlock()
	s.lastContent = initialContent
	if stillOpen {
		if err := s.persistActiveStreamRecoveryLocked(stream, reply); err != nil {
			log.Printf("[terminal-outbox] failed to persist active progress card recovery: %v", err)
		}
		s.registerStreamRecoveryChangeHandlerLocked(stream)
	}
	return stream
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
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
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
	reply := s.currentTerminalReply()
	if reply == nil {
		return
	}
	if err := reply.Typing(s.ctx, true); err != nil {
		log.Printf("[handler] failed to send typing state: %v", err)
	}
}

func (s *progressSession) cancelTyping() {
	reply := s.currentTerminalReply()
	if reply == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := reply.Typing(ctx, false); err != nil {
		log.Printf("[handler] failed to send typing cancel: %v", err)
	}
}
