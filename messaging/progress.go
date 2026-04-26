package messaging

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
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
	handler      *Handler
	ctx          context.Context
	cancel       context.CancelFunc
	client       *ilink.Client
	userID       string
	contextToken string
	prefix       string
	taskText     string
	cfg          config.ProgressConfig
	deltaCh      chan string
	wg           sync.WaitGroup
}

// startProgressSession 启动微信侧进度会话，最终回复仍由调用方单独发送。
func (h *Handler) startProgressSession(ctx context.Context, client *ilink.Client, userID string, contextToken string, prefix string, taskText string, cfg config.ProgressConfig) (func(string), func()) {
	if cfg.Mode == "" {
		cfg = config.DefaultProgressConfig()
	}
	if cfg.Mode == progressModeOff {
		return func(string) {}, func() {}
	}

	progressCtx, cancel := context.WithCancel(ctx)
	session := &progressSession{
		handler: h, ctx: progressCtx, cancel: cancel, client: client,
		userID: userID, contextToken: contextToken, prefix: prefix,
		taskText: taskText, cfg: cfg, deltaCh: make(chan string, 256),
	}
	session.start()
	return session.onProgress, session.stop
}

func (s *progressSession) start() {
	if boolValue(s.cfg.SendAcceptance) {
		title := progressTaskTitle(s.taskText, 60)
		s.send(renderAcceptance(title))
	}
	if boolValue(s.cfg.EnableTyping) {
		s.wg.Add(1)
		go s.runTyping()
	}
	if progressModeAllowsProgress(s.cfg.Mode) {
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

func (s *progressSession) stop() {
	s.cancel()
	s.wg.Wait()
	if boolValue(s.cfg.EnableTyping) {
		s.cancelTyping()
	}
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
	s.handler.sendProgressMessage(s.ctx, s.client, s.userID, s.contextToken, s.prefix+text)
}

func (s *progressSession) sendTyping() {
	if err := SendTypingState(s.ctx, s.client, s.userID, s.contextToken); err != nil {
		log.Printf("[handler] failed to send typing state: %v", err)
	}
}

func (s *progressSession) cancelTyping() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := SendTypingCancel(ctx, s.client, s.userID, s.contextToken); err != nil {
		log.Printf("[handler] failed to send typing cancel: %v", err)
	}
}

func progressModeAllowsProgress(mode string) bool {
	switch mode {
	case progressModeSummary, progressModeVerbose, progressModeStream, progressModeDebug:
		return true
	default:
		return false
	}
}

func progressTaskTitle(text string, maxRunes int) string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "…"
}

func renderAcceptance(taskTitle string) string {
	return "收到，开始处理....."
}

func renderInitialProgress() string {
	return "进展：任务仍在执行中，连接正常。\n\n我会继续等待 Agent 完成，并发送最终完整结果。"
}

func renderDeltaProgress(delta string, cfg config.ProgressConfig) string {
	if cfg.Mode == progressModeStream {
		preview := truncateTailRunes(strings.TrimSpace(delta), cfg.PreviewRunes)
		return "实时片段，仅供预览：\n" + preview
	}
	return "处理中，请耐心等待....."
}

func shouldSendProgress(now time.Time, state progressSendState, summary string, cfg config.ProgressConfig) bool {
	if strings.TrimSpace(summary) == "" {
		return false
	}
	if cfg.MaxProgressMessages > 0 && state.sentCount >= cfg.MaxProgressMessages {
		return false
	}
	if summary != state.lastSentSummary {
		return true
	}
	interval := durationSeconds(cfg.SummaryIntervalSeconds, 0)
	return interval <= 0 || now.Sub(state.lastSentAt) >= interval
}

func renderFinalSuccess(prefix string, reply string) string {
	reply = strings.TrimSpace(reply)
	return prefix + reply
}

func renderFinalFailure(prefix string, err error) string {
	reason := "未知错误"
	if err != nil {
		reason = strings.TrimSpace(err.Error())
	}
	return prefix + "本次未完成。\n\n原因：" + reason + "\n\n你可以调整需求后重试，或发送 /new 开启新会话。"
}

func progressTickerInterval(cfg config.ProgressConfig) time.Duration {
	if cfg.SummaryIntervalSeconds <= 0 {
		return 10 * time.Millisecond
	}
	return time.Duration(cfg.SummaryIntervalSeconds) * time.Second
}

func durationSeconds(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func boolValue(v *bool) bool {
	return v != nil && *v
}

func truncateTailRunes(text string, limit int) string {
	if limit <= 0 || text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[len(runes)-limit:])
}
