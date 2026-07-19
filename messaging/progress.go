package messaging

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
}

type progressSession struct {
	handler             *Handler
	ctx                 context.Context
	cancel              context.CancelFunc
	reply               platform.Replier
	stream              platform.Stream
	prefix              string
	agentName           string
	workspaceRoot       string
	taskText            string
	cfg                 config.ProgressConfig
	deltaCh             chan string
	wg                  sync.WaitGroup
	streamMu            sync.Mutex
	streamOpenAttempted bool
	typingStarted       bool
}

// startProgressSession 启动平台进度会话，保持旧语义：最终回复由调用方单独发送。
func (h *Handler) startProgressSession(ctx context.Context, reply platform.Replier, prefix string, taskText string, cfg config.ProgressConfig) (func(string), func()) {
	onProgress, finish := h.startProgressSessionWithFinal(ctx, reply, prefix, taskText, cfg)
	return onProgress, func() {
		_ = finish(progressDefaultCompletion, false)
	}
}

// startProgressSessionWithFinal 启动进度会话，并允许原生流式平台把最终结果收敛进同一张卡片。
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
	if s.cfg.InitialDelaySeconds <= 0 {
		s.ensureStream()
	}
	select {
	case s.deltaCh <- delta:
	case <-s.ctx.Done():
	default:
	}
}

func (s *progressSession) stopWithFinal(finalText string, failed bool) bool {
	parentCanceled := s.stopBackground()
	return s.finishStream(parentCanceled, finalText, failed)
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

type preparedProgressTerminal struct {
	checkpoint   *platform.TerminalCheckpoint
	consumed     bool
	notification string
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

func (s *progressSession) prepareDurableTerminal(replyWriter platform.Replier, finalText string, failed bool) (preparedProgressTerminal, error) {
	if s == nil {
		return preparedProgressTerminal{}, nil
	}
	parentCanceled := s.stopBackground()
	s.streamMu.Lock()
	stream := s.stream
	s.streamMu.Unlock()
	if stream == nil || !s.reply.Capabilities().Streaming {
		return preparedProgressTerminal{}, nil
	}
	durable, ok := stream.(platform.DurableTerminalStream)
	if !ok {
		return preparedProgressTerminal{}, platform.ErrUnsupported
	}
	content, terminalFailed, consumed := progressTerminalArguments(replyWriter, parentCanceled, finalText, failed)
	checkpoint, err := durable.PrepareTerminal(content, terminalFailed)
	if err != nil {
		return preparedProgressTerminal{}, err
	}
	prepared := preparedProgressTerminal{
		checkpoint:   &checkpoint,
		consumed:     consumed,
		notification: renderStreamTerminalNotification(parentCanceled, failed, finalText),
	}
	return prepared, nil
}

func progressTerminalArguments(replyWriter platform.Replier, parentCanceled bool, finalText string, failed bool) (string, bool, bool) {
	terminalFailed := parentCanceled || failed
	if finalText == progressStatusOnlyComplete {
		return "", terminalFailed, false
	}
	if shouldKeepFinalReplyOutsideStream(replyWriter, finalText) {
		if failed {
			return firstNonBlank(finalText, "任务执行失败。"), true, false
		}
		return "", terminalFailed, false
	}
	if !canConsumeFinalReplyInStream(finalText) {
		fallback := progressDefaultCompletion
		if parentCanceled {
			fallback = "任务已停止。"
		} else if failed {
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
		case now := <-ticker.C:
			s.handleTimedProgress(now, startedAt, &stageIndex, &state)
		}
	}
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
	now := time.Now()
	if !shouldSendProgress(now, *state, summary, s.cfg) {
		return
	}
	if !s.send(summary) {
		return
	}
	state.lastSentSummary = summary
	state.lastSentAt = now
	state.sentCount++
}

func (s *progressSession) send(text string) bool {
	stream := s.ensureStream()
	if stream != nil {
		if err := stream.Update(s.ctx, s.prefix+text); err != nil {
			log.Printf("[handler] failed to update progress stream: %v", err)
			return false
		}
		return true
	}
	if s.reply.Capabilities().Streaming {
		return false
	}
	return s.sendText(text)
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
	return stream
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

func (s *progressSession) finishStream(parentCanceled bool, finalText string, failed bool) bool {
	s.streamMu.Lock()
	stream := s.stream
	s.streamMu.Unlock()
	if stream == nil || !s.reply.Capabilities().Streaming {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	switch {
	case parentCanceled:
		err = stream.Fail(ctx, firstNonBlank(finalText, "任务已停止。"))
	case failed:
		err = stream.Fail(ctx, firstNonBlank(finalText, "任务执行失败。"))
	case finalText == progressStatusOnlyComplete:
		err = stream.Complete(ctx, "")
	case strings.TrimSpace(finalText) != "":
		err = stream.Complete(ctx, finalText)
	default:
		err = stream.Complete(ctx, progressDefaultCompletion)
	}
	if err != nil {
		log.Printf("[handler] failed to finish progress stream: %v", err)
		return false
	}
	notification := renderStreamTerminalNotification(parentCanceled, failed, finalText)
	if notification != "" && s.reply.Capabilities().StreamCompletionNotification {
		if notifyErr := s.reply.SendText(ctx, notification); notifyErr != nil {
			log.Printf("[handler] failed to send stream terminal notification: %v", notifyErr)
		}
	}
	return strings.TrimSpace(finalText) != "" && finalText != progressStatusOnlyComplete
}
