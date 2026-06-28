package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

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
	if strings.Contains(got, "实时片段") {
		t.Fatalf("summary progress should not contain realtime snippet label, got %q", got)
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

	if !strings.Contains(got, "Agent 会话已失效") ||
		!strings.Contains(got, "请发送 /new") ||
		!strings.Contains(got, "重启或切换账号") {
		t.Fatalf("session not found should include explicit recovery hint, got %q", got)
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

func TestStreamModeRendersTextPreview(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.PreviewRunes = 4

	got := renderDeltaProgress("第一段第二段第三段", cfg)

	if !strings.Contains(got, "实时片段，仅供预览") {
		t.Fatalf("stream progress should contain preview label, got %q", got)
	}
	if !strings.Contains(got, "第三段") {
		t.Fatalf("stream progress should contain tail preview, got %q", got)
	}
	if strings.Contains(got, "第一段") {
		t.Fatalf("stream progress should truncate old text, got %q", got)
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
