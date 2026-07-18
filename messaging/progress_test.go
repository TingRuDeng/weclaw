package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestRenderFinalFailureExplainsAgentSessionNotBound(t *testing.T) {
	got := renderFinalFailure("", agent.ErrAgentSessionNotBound)

	if !strings.Contains(got, "当前窗口尚未绑定会话") ||
		!strings.Contains(got, "选择已有会话") ||
		!strings.Contains(got, "发送 /new") {
		t.Fatalf("session not bound should include explicit choices, got %q", got)
	}
}

func TestRenderFinalFailureExplainsCodexTransportDisconnectWithoutClearingBinding(t *testing.T) {
	got := renderFinalFailure("", agent.ErrCodexDesktopDisconnected)

	if !strings.Contains(got, "Codex 运行通道暂不可用") ||
		!strings.Contains(got, "窗口绑定保持不变") ||
		!strings.Contains(got, "/cx status") {
		t.Fatalf("Desktop disconnect should preserve owner and provide recovery hint, got %q", got)
	}
	if strings.Contains(got, "Codex Desktop 连接已断开") {
		t.Fatalf("Desktop disconnect should not expose raw transport error, got %q", got)
	}
	if strings.Contains(got, "发送 /new") {
		t.Fatalf("Desktop disconnect should keep the selected session, got %q", got)
	}
}

func TestRenderFinalFailureWarnsWhenCodexDeliveryIsUnknown(t *testing.T) {
	err := errors.Join(agent.ErrCodexDesktopDeliveryUnknown, agent.ErrCodexDesktopDisconnected)
	got := renderFinalFailure("", err)

	if !strings.Contains(got, "任务是否已开始暂时无法确认") ||
		!strings.Contains(got, "窗口绑定保持不变") ||
		!strings.Contains(got, "避免重复提交") ||
		!strings.Contains(got, "/cx status") {
		t.Fatalf("unknown delivery should warn before retry, got %q", got)
	}
	if strings.Contains(got, "发送 /new") {
		t.Fatalf("unknown delivery should keep the selected session, got %q", got)
	}
}

func TestRenderAcceptance(t *testing.T) {
	taskTitle := progressTaskTitle("修复 WeClaw 里 Codex 实时回复碎片化的问题，并保留 stream 兼容模式", 13)
	got := renderAcceptance(taskTitle)

	if got != "收到，开始处理....." {
		t.Fatalf("acceptance=%q, want short acceptance", got)
	}
	if strings.Contains(got, taskTitle) {
		t.Fatalf("acceptance should not repeat task title, got %q", got)
	}
}

func TestSummaryModeDoesNotRenderTextPreview(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeSummary
	cfg.ShowTextPreview = boolPtr(true)
	delta := "这里是一段 Codex 正文 delta"

	got := renderDeltaProgress(delta, cfg)

	if strings.Contains(got, delta) {
		t.Fatalf("summary progress should not contain raw delta, got %q", got)
	}
	if strings.Contains(got, "实时状态") {
		t.Fatalf("summary progress should not contain realtime status label, got %q", got)
	}
	if got != "处理中，请耐心等待....." {
		t.Fatalf("summary progress=%q, want short waiting text", got)
	}
}

func TestRenderFinalSuccessReturnsReplyWithoutWrapperAndKeepsNewlines(t *testing.T) {
	reply := "🧩 步骤：查询当前工作目录\n🎯 目的：准确返回你当前会话路径\n▶️ 执行：运行 pwd 命令。\n/Volumes/Data/code/MyCode"

	got := renderFinalSuccess("", reply)

	if strings.Contains(got, "已完成，以下是完整结果") {
		t.Fatalf("final success should not contain wrapper, got %q", got)
	}
	if got != reply {
		t.Fatalf("final success=%q, want original reply", got)
	}
	if !strings.Contains(got, "\n🎯 目的") {
		t.Fatalf("final success should keep newlines, got %q", got)
	}
}

func TestRenderFinalFailureExplainsCodexUpstreamError(t *testing.T) {
	err := errors.New(`turn error: Error running remote compact task: unexpected status 502 Bad Gateway: OpenAIException - {"error":{"message":"Upstream service temporarily unavailable","type":"upstream_error"}}`)

	got := renderFinalFailure("", err)

	if !strings.Contains(got, "Codex 上游服务暂时不可用") ||
		!strings.Contains(got, "这通常不是微信或 WeClaw 配置错误") ||
		!strings.Contains(got, "/new") {
		t.Fatalf("codex upstream failure should be explained clearly, got %q", got)
	}
	if strings.Contains(got, "OpenAIException") || strings.Contains(got, "BadGatewayError") {
		t.Fatalf("codex upstream failure should hide noisy provider internals, got %q", got)
	}
}

func TestRenderFinalFailureExplainsACPSessionNotFound(t *testing.T) {
	err := errors.New("prompt error: agent error: Session not found")

	got := renderFinalFailure("", err)

	if !strings.Contains(got, "原会话无法恢复") ||
		!strings.Contains(got, "切换其他会话") ||
		!strings.Contains(got, "发送 /new") {
		t.Fatalf("session not found should include explicit recovery hint, got %q", got)
	}
}

func TestRenderFinalFailureExplainsCodexThreadNotFound(t *testing.T) {
	err := errors.New("thread error: resume restored thread old-thread: thread not found")

	got := renderFinalFailure("", err)

	if !strings.Contains(got, "原会话无法恢复") ||
		!strings.Contains(got, "切换其他会话") ||
		!strings.Contains(got, "发送 /new") {
		t.Fatalf("thread not found should include explicit recovery hint, got %q", got)
	}
}

func TestRenderFinalFailureExplainsCodexWebSocketForbidden(t *testing.T) {
	err := errors.New("turn error: \x1b[2m2026-05-21T09:02:00Z\x1b[0m ERROR codex_api::endpoint::responses_websocket: failed to connect to websocket: HTTP error: 403 Forbidden, url: ws://192.168.201.10:4000/v1/responses")

	got := renderFinalFailure("", err)

	if !strings.Contains(got, "Codex 实时通道连接被服务端拒绝") ||
		!strings.Contains(got, "403 Forbidden") ||
		!strings.Contains(got, "HTTPS 通道重试") {
		t.Fatalf("websocket 403 should include clear transport hint, got %q", got)
	}
	if strings.Contains(got, "\x1b[") || strings.Contains(got, "codex_api::endpoint") {
		t.Fatalf("websocket 403 should hide ansi and internal module details, got %q", got)
	}
}

func TestRenderFinalFailureStripsANSIForUnknownAgentError(t *testing.T) {
	err := errors.New("turn error: \x1b[31munknown provider failure\x1b[0m")

	got := renderFinalFailure("", err)

	if strings.Contains(got, "\x1b[") {
		t.Fatalf("unknown agent error should strip ansi control sequences, got %q", got)
	}
	if !strings.Contains(got, "unknown provider failure") {
		t.Fatalf("unknown agent error should keep useful text, got %q", got)
	}
}

func TestStreamModeRendersLastNonEmptyStatusLine(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.PreviewRunes = 80

	got := renderDeltaProgress("正在分析代码\n\n正在运行 go test ./messaging\n", cfg)

	if got != "正在运行 go test ./messaging" {
		t.Fatalf("stream progress should be latest non-empty line only, got %q", got)
	}
	for _, stale := range []string{"正在分析代码", "实时状态", "实时片段"} {
		if strings.Contains(got, stale) {
			t.Fatalf("stream progress should not contain stale or wrapper text %q, got %q", stale, got)
		}
	}
}

func TestStreamModeTruncatesLongStatusLineToLatestTail(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.PreviewRunes = 4

	got := renderDeltaProgress("第一段第二段第三段", cfg)

	if !strings.Contains(got, "第三段") {
		t.Fatalf("stream progress should keep latest tail of long status line, got %q", got)
	}
	if strings.Contains(got, "第一段") {
		t.Fatalf("stream progress should drop stale head of long status line, got %q", got)
	}
}

func TestStreamModeKeepsStructuredProgressStatus(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	status := "进展：Codex 已产生代码变更。"

	got := renderDeltaProgress(status, cfg)

	if got != status {
		t.Fatalf("structured progress status=%q, want %q", got, status)
	}
	if strings.Contains(got, "实时状态") {
		t.Fatalf("structured progress should not be rendered as realtime status, got %q", got)
	}
}

func TestStreamModeKeepsOnlyLatestStructuredProgressOnSameLine(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	delta := "进展：Codex 已产生代码或文件变更。进展：Codex 已产生代码或文件变更。进展：Codex 已产生代码或文件变更。"

	got := renderDeltaProgress(delta, cfg)

	want := "进展：Codex 已产生代码或文件变更。"
	if got != want {
		t.Fatalf("structured progress=%q, want latest single status %q", got, want)
	}
	if strings.Count(got, "进展：") != 1 {
		t.Fatalf("structured progress should contain one status marker, got %q", got)
	}
}

func TestStreamModePrefersStructuredProgressOverFinalMarkdown(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.PreviewRunes = 120
	delta := strings.Join([]string{
		"进展：修复实时状态渲染",
		"",
		"## 交付说明",
		"",
		"### 做了什么",
		"- 这是一段最终回复正文",
	}, "\n")

	got := renderDeltaProgress(delta, cfg)

	if got != "进展：修复实时状态渲染" {
		t.Fatalf("stream progress=%q, want structured progress", got)
	}
	if strings.Contains(got, "最终回复正文") {
		t.Fatalf("stream progress must not show final answer line, got %q", got)
	}
}

func TestProgressThrottleByInterval(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.SummaryIntervalSeconds = 20
	state := progressSendState{
		lastSentAt:      time.Unix(100, 0),
		lastSentSummary: "进展：任务仍在执行中，连接正常。",
	}

	if shouldSendProgress(time.Unix(110, 0), state, "进展：任务仍在执行中，连接正常。", cfg) {
		t.Fatal("same summary inside interval should not send")
	}
	if !shouldSendProgress(time.Unix(121, 0), state, "进展：任务仍在执行中，连接正常。", cfg) {
		t.Fatal("same summary after interval should send")
	}
	if !shouldSendProgress(time.Unix(110, 0), state, "进展：Agent 正在生成和整理结果。", cfg) {
		t.Fatal("different summary should send without waiting full interval")
	}
}

func TestProgressMaxMessages(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.MaxProgressMessages = 2
	state := progressSendState{sentCount: 2}

	if shouldSendProgress(time.Now(), state, "进展：任务仍在执行中，连接正常。", cfg) {
		t.Fatal("progress should stop after max progress messages")
	}
}

func TestStreamProgressIgnoresMessageLimitAndKeepsLatestUpdate(t *testing.T) {
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.MaxProgressMessages = 4
	session := &progressSession{
		ctx: context.Background(), reply: reply, taskText: "长任务", cfg: cfg,
	}
	state := progressSendState{}
	progresses := []string{"进展一", "进展二", "进展三", "进展四", "进展五", "进展六"}

	for _, progress := range progresses {
		session.sendProgressIfAllowed(progress, &state)
	}

	if len(reply.Stream.Updates) != len(progresses) {
		t.Fatalf("stream updates=%#v, want all %d progress updates", reply.Stream.Updates, len(progresses))
	}
	if got := reply.Stream.Updates[len(reply.Stream.Updates)-1]; got != "进展六" {
		t.Fatalf("latest stream update=%q, want 进展六", got)
	}
}

func TestFailedProgressUpdateDoesNotConsumeMessageLimit(t *testing.T) {
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	reply.Stream.UpdateErr = errors.New("update failed")
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	session := &progressSession{
		ctx: context.Background(), reply: reply, taskText: "长任务", cfg: cfg,
	}
	state := progressSendState{}

	session.sendProgressIfAllowed("进展一", &state)

	if state.sentCount != 0 {
		t.Fatalf("sent count=%d, failed update must not consume progress limit", state.sentCount)
	}
	if state.lastSentSummary != "" {
		t.Fatalf("last sent summary=%q, failed update must remain retryable", state.lastSentSummary)
	}
}

func TestProgressDedupSameSummary(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.SummaryIntervalSeconds = 20
	state := progressSendState{
		lastSentAt:      time.Now(),
		lastSentSummary: "进展：任务仍在执行中，连接正常。",
	}

	if shouldSendProgress(time.Now(), state, "进展：任务仍在执行中，连接正常。", cfg) {
		t.Fatal("same summary should be deduped inside interval")
	}
	if !shouldSendProgress(time.Now(), state, "进展：仍在持续执行，请稍等最终结果。", cfg) {
		t.Fatal("different summary should be allowed")
	}
}

func TestNativeStreamProgressSkipsTypingAndCompletes(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Typing: true, Streaming: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.InitialDelaySeconds = 0
	cfg.EnableTyping = boolPtr(true)

	onProgress, stop := h.startProgressSession(context.Background(), reply, "", "飞书流式任务", cfg)
	onProgress("部分结果")
	stop()

	if len(reply.TypingStates) != 0 {
		t.Fatalf("typing states=%#v, want native stream without typing", reply.TypingStates)
	}
	if reply.Stream.Completed == "" {
		t.Fatal("native progress stream should be completed on stop")
	}
	if reply.Stream.Failed != "" {
		t.Fatalf("stream failed=%q, want no failure", reply.Stream.Failed)
	}
}

func TestNativeStreamProgressCompletesWithFinalResult(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Typing: true, Streaming: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.InitialDelaySeconds = 0

	onProgress, finish := h.startProgressSessionWithFinal(context.Background(), reply, "", "飞书流式任务", cfg)
	onProgress("临时过程")
	consumed := finish("最终结果", false)

	if !consumed {
		t.Fatal("native stream should consume final result")
	}
	if reply.Stream.Completed != "最终结果" {
		t.Fatalf("completed=%q, want final result", reply.Stream.Completed)
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("texts=%#v, want no extra text", reply.Texts)
	}
}

func TestNativeStreamOpensBeforeFirstAgentProgress(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream

	_, finish := h.startProgressSessionWithFinal(context.Background(), reply, "", "短任务", cfg)
	if reply.Stream.Options.Title != "短任务" {
		t.Fatalf("stream options = %#v", reply.Stream.Options)
	}
	if reply.Stream.Options.InitialContent != "正在处理任务，请稍候。" {
		t.Fatalf("initial content = %q", reply.Stream.Options.InitialContent)
	}
	consumed := finish("最终结果", false)
	if !consumed || reply.Stream.Completed != "最终结果" {
		t.Fatalf("consumed = %v, stream = %#v", consumed, reply.Stream)
	}
}

func TestNativeTaskCardTitleIncludesAgentSource(t *testing.T) {
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream

	_, finish := NewHandler(nil, nil).startProgressSessionForAgentWithFinal(
		context.Background(), reply, "", "claude-agent-acp", "修复登录流程", cfg,
	)
	if got := reply.Stream.Options.Title; got != "Claude · 修复登录流程" {
		t.Fatalf("title=%q，期望带 Claude 来源前缀", got)
	}
	finish("完成", false)
}

func TestNativeStreamCreationFailureIsExplicitAndNotRetried(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	reply.OpenStreamErr = errors.New("card unavailable")
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.InitialDelaySeconds = 0

	onProgress, finish := h.startProgressSessionWithFinal(context.Background(), reply, "", "任务", cfg)
	onProgress("进展：正在分析。")
	if finish("最终结果", false) {
		t.Fatal("failed stream must not consume final reply")
	}
	want := "任务已开始，卡片创建失败，将以普通消息返回结果。"
	if len(reply.Texts) != 1 || reply.Texts[0] != want {
		t.Fatalf("texts = %#v", reply.Texts)
	}
	if reply.OpenStreamCalls != 1 {
		t.Fatalf("OpenStream calls = %d, want 1", reply.OpenStreamCalls)
	}
}

func TestProgressOffModeDoesNotOpenNativeStream(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff

	_, finish := h.startProgressSessionWithFinal(context.Background(), reply, "", "任务", cfg)
	if finish("最终结果", false) {
		t.Fatal("off mode must not consume final reply")
	}
	if reply.OpenStreamCalls != 0 {
		t.Fatalf("OpenStream calls = %d, want 0", reply.OpenStreamCalls)
	}
}

func TestNativeStreamTerminalNotifications(t *testing.T) {
	tests := []struct {
		name   string
		cancel bool
		failed bool
		want   string
	}{
		{name: "success", want: "任务已完成，请查看上方卡片。"},
		{name: "failure", failed: true, want: "任务执行失败，请查看上方卡片。"},
		{name: "stopped", cancel: true, want: "任务已停止，请查看上方卡片。"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reply := platformtest.NewReplier(platform.Capabilities{
				Text: true, Streaming: true, StreamCompletionNotification: true,
			})
			cfg := config.DefaultProgressConfig()
			cfg.Mode = progressModeStream
			_, finish := NewHandler(nil, nil).startProgressSessionWithFinal(ctx, reply, "", "任务", cfg)
			if tc.cancel {
				cancel()
			}
			finish("终态正文", tc.failed)
			if len(reply.Texts) != 1 || reply.Texts[0] != tc.want {
				t.Fatalf("texts = %#v", reply.Texts)
			}
		})
	}
}

func TestNativeStreamTerminalFailureDoesNotNotify(t *testing.T) {
	reply := platformtest.NewReplier(platform.Capabilities{
		Text: true, Streaming: true, StreamCompletionNotification: true,
	})
	reply.Stream.CompleteErr = errors.New("update failed")
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	_, finish := NewHandler(nil, nil).startProgressSessionWithFinal(context.Background(), reply, "", "任务", cfg)

	if finish("完整结果", false) {
		t.Fatal("failed terminal update must not consume final reply")
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("texts = %#v", reply.Texts)
	}
}
