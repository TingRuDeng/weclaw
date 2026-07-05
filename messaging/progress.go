package messaging

import (
	"context"
	"log"
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
	handler       *Handler
	ctx           context.Context
	cancel        context.CancelFunc
	reply         platform.Replier
	stream        platform.Stream
	prefix        string
	taskText      string
	cfg           config.ProgressConfig
	deltaCh       chan string
	wg            sync.WaitGroup
	typingStarted bool
}

// startProgressSession 启动平台进度会话，保持旧语义：最终回复由调用方单独发送。
func (h *Handler) startProgressSession(ctx context.Context, reply platform.Replier, prefix string, taskText string, cfg config.ProgressConfig) (func(string), func()) {
	onProgress, finish := h.startProgressSessionWithFinal(ctx, reply, prefix, taskText, cfg)
	return onProgress, func() {
		_ = finish("", false)
	}
}

// startProgressSessionWithFinal 启动进度会话，并允许原生流式平台把最终结果收敛进同一张卡片。
func (h *Handler) startProgressSessionWithFinal(ctx context.Context, reply platform.Replier, prefix string, taskText string, cfg config.ProgressConfig) (func(string), func(string, bool) bool) {
	if cfg.Mode == "" {
		cfg = config.DefaultProgressConfig()
	}
	if cfg.Mode == progressModeOff {
		return func(string) {}, func(string, bool) bool { return false }
	}

	progressCtx, cancel := context.WithCancel(ctx)
	session := &progressSession{
		handler: h, ctx: progressCtx, cancel: cancel, reply: reply,
		prefix: prefix, taskText: taskText, cfg: cfg, deltaCh: make(chan string, 256),
	}
	session.start()
	return session.onProgress, session.stopWithFinal
}

func (s *progressSession) start() {
	if boolValue(s.cfg.SendAcceptance) {
		title := progressTaskTitle(s.taskText, 60)
		s.sendText(renderAcceptance(title))
	}
	usesNativeProgress := progressModeAllowsProgress(s.cfg.Mode) && s.reply.Capabilities().Streaming
	if boolValue(s.cfg.EnableTyping) && !usesNativeProgress {
		s.typingStarted = true
		s.wg.Add(1)
		go s.runTyping()
	}
	if progressModeAllowsProgress(s.cfg.Mode) {
		s.openStream()
		s.wg.Add(1)
		go s.runProgressLoop()
	}
}

func (s *progressSession) onProgress(delta string) {
	select {
	case s.deltaCh <- delta:
	case <-s.ctx.Done():
	default:
	}
}

func (s *progressSession) stopWithFinal(finalText string, failed bool) bool {
	parentCanceled := s.ctx.Err() != nil
	s.cancel()
	s.wg.Wait()
	if s.typingStarted {
		s.cancelTyping()
	}
	return s.finishStream(parentCanceled, finalText, failed)
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
	summary := renderDeltaProgress(tail, s.cfg)
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
	s.send(summary)
	state.lastSentSummary = summary
	state.lastSentAt = now
	state.sentCount++
}

func (s *progressSession) send(text string) {
	if s.stream != nil {
		if err := s.stream.Update(s.ctx, s.prefix+text); err != nil {
			log.Printf("[handler] failed to update progress stream: %v", err)
		}
		return
	}
	s.sendText(text)
}

func (s *progressSession) sendText(text string) {
	if err := s.reply.SendText(s.ctx, s.prefix+text); err != nil {
		log.Printf("[handler] failed to send progress message: %v", err)
	}
}

func (s *progressSession) openStream() {
	stream, err := s.reply.OpenStream(s.ctx, platform.StreamOptions{Title: progressTaskTitle(s.taskText, 60)})
	if err != nil {
		log.Printf("[handler] failed to open progress stream: %v", err)
		return
	}
	s.stream = stream
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
	if s.stream == nil || !s.reply.Capabilities().Streaming {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	switch {
	case parentCanceled:
		err = s.stream.Fail(ctx, firstNonBlank(finalText, "任务已停止。"))
	case failed:
		err = s.stream.Fail(ctx, firstNonBlank(finalText, "任务执行失败。"))
	case strings.TrimSpace(finalText) != "":
		err = s.stream.Complete(ctx, finalText)
	default:
		err = s.stream.Complete(ctx, "任务已完成，正在发送最终结果。")
	}
	if err != nil {
		log.Printf("[handler] failed to finish progress stream: %v", err)
		return false
	}
	return strings.TrimSpace(finalText) != ""
}
