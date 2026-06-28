package messaging

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

// startProgressSession 启动平台进度会话，最终回复仍由调用方单独发送。
func (h *Handler) startProgressSession(ctx context.Context, reply platform.Replier, prefix string, taskText string, cfg config.ProgressConfig) (func(string), func()) {
	if cfg.Mode == "" {
		cfg = config.DefaultProgressConfig()
	}
	if cfg.Mode == progressModeOff {
		return func(string) {}, func() {}
	}

	progressCtx, cancel := context.WithCancel(ctx)
	session := &progressSession{
		handler: h, ctx: progressCtx, cancel: cancel, reply: reply,
		prefix: prefix, taskText: taskText, cfg: cfg, deltaCh: make(chan string, 256),
	}
	session.start()
	return session.onProgress, session.stop
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

func (s *progressSession) stop() {
	parentCanceled := s.ctx.Err() != nil
	s.cancel()
	s.wg.Wait()
	if s.typingStarted {
		s.cancelTyping()
	}
	s.finishStream(parentCanceled)
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

func (s *progressSession) finishStream(parentCanceled bool) {
	if s.stream == nil || !s.reply.Capabilities().Streaming {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if parentCanceled {
		err = s.stream.Fail(ctx, "任务已停止。")
	} else {
		err = s.stream.Complete(ctx, "任务已完成，正在发送最终结果。")
	}
	if err != nil {
		log.Printf("[handler] failed to finish progress stream: %v", err)
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
		reason = friendlyAgentError(err)
	}
	return prefix + "本次未完成。\n\n原因：" + reason + "\n\n你可以调整需求后重试，或发送 /new 开启新会话。"
}

// friendlyAgentError 将常见 Agent 底层错误转换成微信侧可操作提示。
func friendlyAgentError(err error) string {
	raw := sanitizeAgentError(err.Error())
	lower := strings.ToLower(raw)
	switch {
	case isCodexUpstreamError(lower):
		return "Codex 上游服务暂时不可用，当前请求没有完成。这通常不是微信或 WeClaw 配置错误，可以稍后重试；如果同一个旧会话反复触发 compact 失败，请发送 /new 创建新会话。"
	case isCodexWebSocketForbidden(lower):
		return "Codex 实时通道连接被服务端拒绝（403 Forbidden）。这是 Codex 网关的 WebSocket 权限或代理配置问题；Codex 通常会尝试 HTTPS 通道重试，如果仍失败，请检查当前 Codex 网关的 responses WebSocket 访问权限。"
	case isACPSessionNotFound(lower):
		return "Agent 会话已失效，可能是 ACP 子进程重启或切换账号后，本地恢复了旧 sessionId。请发送 /new 创建新会话后再试。"
	default:
		return raw
	}
}

// sanitizeAgentError 清理终端控制字符，避免 ANSI 颜色码透出到微信消息。
func sanitizeAgentError(text string) string {
	text = ansiEscapePattern.ReplaceAllString(text, "")
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, text)
	return strings.TrimSpace(text)
}

func isCodexUpstreamError(lower string) bool {
	hasCodexSignal := strings.Contains(lower, "turn error") ||
		strings.Contains(lower, "remote compact") ||
		strings.Contains(lower, "/responses/compact")
	hasUpstreamSignal := strings.Contains(lower, "upstream") ||
		strings.Contains(lower, "bad gateway") ||
		strings.Contains(lower, "502")
	return hasCodexSignal && hasUpstreamSignal
}

func isCodexWebSocketForbidden(lower string) bool {
	hasCodexSignal := strings.Contains(lower, "responses_websocket") ||
		strings.Contains(lower, "/v1/responses") ||
		strings.Contains(lower, "ws://")
	hasForbiddenSignal := strings.Contains(lower, "websocket") &&
		strings.Contains(lower, "403 forbidden")
	return hasCodexSignal && hasForbiddenSignal
}

func isACPSessionNotFound(lower string) bool {
	hasPromptSignal := strings.Contains(lower, "prompt error") ||
		strings.Contains(lower, "session/prompt") ||
		strings.Contains(lower, "agent error")
	return hasPromptSignal && strings.Contains(lower, "session not found")
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
