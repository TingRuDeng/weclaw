package messaging

import (
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
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
