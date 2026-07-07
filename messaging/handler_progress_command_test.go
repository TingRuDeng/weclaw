package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestHandleProgressCommandShowsCurrentMode(t *testing.T) {
	h := NewHandler(nil, nil)

	reply := h.handleProgressCommand("/progress")

	if !strings.Contains(reply, "当前进度模式：typing") {
		t.Fatalf("reply=%q, want current typing mode", reply)
	}
}

func TestHandleProgressCommandChangesMode(t *testing.T) {
	h := NewHandler(nil, nil)

	reply := h.handleProgressCommand("/progress stream")

	if !strings.Contains(reply, "已切换进度模式：stream") {
		t.Fatalf("reply=%q, want switched stream mode", reply)
	}
	if got := h.resolveProgressConfig("").Mode; got != progressModeStream {
		t.Fatalf("progress mode=%q, want stream", got)
	}
}

func TestProgressCommandShowsPlatformEffectiveMode(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		string(platform.PlatformFeishu): {Mode: progressModeStream},
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.handleBuiltInPlatformCommand(context.Background(), platformCommandRequest{
		Message: platform.IncomingMessage{
			Platform: platform.PlatformFeishu,
			UserID:   "ou_user",
			Text:     "/progress",
		},
		RouteUserID: "ou_user",
		Reply:       reply,
		Trimmed:     "/progress",
	})

	if !containsText(reply.Texts, "当前进度模式：stream") {
		t.Fatalf("reply=%#v, want feishu effective stream mode", reply.Texts)
	}
}

func TestProgressCommandChangesPlatformOverride(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		string(platform.PlatformFeishu): {Mode: progressModeStream},
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.handleBuiltInPlatformCommand(context.Background(), platformCommandRequest{
		Message: platform.IncomingMessage{
			Platform: platform.PlatformFeishu,
			UserID:   "ou_user",
			Text:     "/progress typing",
		},
		RouteUserID: "ou_user",
		Reply:       reply,
		Trimmed:     "/progress typing",
	})

	if !containsText(reply.Texts, "已切换进度模式：typing") {
		t.Fatalf("reply=%#v, want switched typing mode", reply.Texts)
	}
	if got := h.resolveProgressConfigForPlatform(platform.PlatformFeishu, "codex").Mode; got != progressModeTyping {
		t.Fatalf("feishu progress mode=%q, want typing", got)
	}
}

func TestProgressCommandChangesOnlyCurrentFeishuAccount(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_a"): {Mode: progressModeSummary},
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_b"): {Mode: progressModeStream},
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.handleBuiltInPlatformCommand(context.Background(), platformCommandRequest{
		Message: platform.IncomingMessage{
			Platform:  platform.PlatformFeishu,
			AccountID: "cli_a",
			UserID:    "ou_user",
			Text:      "/progress typing",
		},
		RouteUserID: "ou_user",
		Reply:       reply,
		Trimmed:     "/progress typing",
	})

	if !containsText(reply.Texts, "已切换进度模式：typing") {
		t.Fatalf("reply=%#v, want switched typing mode", reply.Texts)
	}
	if got := h.resolveProgressConfigForAccount(platform.PlatformFeishu, "cli_a", "codex").Mode; got != progressModeTyping {
		t.Fatalf("cli_a progress mode=%q, want typing", got)
	}
	if got := h.resolveProgressConfigForAccount(platform.PlatformFeishu, "cli_b", "codex").Mode; got != progressModeStream {
		t.Fatalf("cli_b progress mode=%q, want unchanged stream", got)
	}
}

func TestHandleProgressCommandRejectsUnknownMode(t *testing.T) {
	h := NewHandler(nil, nil)

	reply := h.handleProgressCommand("/progress noisy")

	if !strings.Contains(reply, "不支持的进度模式") {
		t.Fatalf("reply=%q, want unsupported mode message", reply)
	}
	if got := h.resolveProgressConfig("").Mode; got != progressModeTyping {
		t.Fatalf("progress mode=%q, want unchanged typing", got)
	}
}

func TestResolveProgressConfigForPlatformUsesPlatformOverride(t *testing.T) {
	h := NewHandler(nil, nil)
	globalCfg := config.DefaultProgressConfig()
	globalCfg.Mode = progressModeTyping
	agentCfg := config.ProgressConfig{Mode: progressModeStream}
	platformCfg := config.ProgressConfig{Mode: progressModeSummary}

	h.SetProgressConfig(globalCfg)
	h.SetAgentProgressConfigs(map[string]config.ProgressConfig{"codex": agentCfg})
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		string(platform.PlatformFeishu): platformCfg,
	})

	got := h.resolveProgressConfigForPlatform(platform.PlatformFeishu, "codex")
	if got.Mode != progressModeSummary {
		t.Fatalf("progress mode=%q, want platform override %q", got.Mode, progressModeSummary)
	}
}

func TestResolveProgressConfigForFeishuDefaultsToSummary(t *testing.T) {
	h := NewHandler(nil, nil)
	globalCfg := config.DefaultProgressConfig()
	globalCfg.Mode = progressModeStream
	h.SetProgressConfig(globalCfg)

	got := h.resolveProgressConfigForPlatform(platform.PlatformFeishu, "codex")

	if got.Mode != progressModeSummary {
		t.Fatalf("feishu progress mode=%q, want quiet summary by default", got.Mode)
	}
}

func TestResolveProgressConfigForFeishuAllowsExplicitStreamOverride(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		string(platform.PlatformFeishu): {Mode: progressModeStream},
	})

	got := h.resolveProgressConfigForPlatform(platform.PlatformFeishu, "codex")

	if got.Mode != progressModeStream {
		t.Fatalf("feishu progress mode=%q, want explicit stream override", got.Mode)
	}
}

func TestResolveProgressConfigKeepsWechatMode(t *testing.T) {
	h := NewHandler(nil, nil)
	globalCfg := config.DefaultProgressConfig()
	globalCfg.Mode = progressModeStream
	h.SetProgressConfig(globalCfg)

	got := h.resolveProgressConfigForPlatform(platform.PlatformWeChat, "codex")

	if got.Mode != progressModeStream {
		t.Fatalf("wechat progress mode=%q, want unchanged stream", got.Mode)
	}
}
