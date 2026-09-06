package cmd

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestFeishuNewBotStreamDefaultPreservesExistingProgress(t *testing.T) {
	for _, mode := range []string{"", "summary", "off"} {
		var existing *config.ProgressConfig
		if mode != "" {
			existing = &config.ProgressConfig{Mode: mode}
		}
		cfg := config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Bots: []config.FeishuBotConfig{{Name: "old", AppID: "old", Progress: existing}}}}}
		upsertFeishuBotConfig(&cfg, feishuBootstrapOptions{Name: "old", AppID: "old"})
		if cfg.Platforms["feishu"].Bots[0].Progress != existing {
			t.Fatal("existing progress changed")
		}
		upsertFeishuBotConfig(&cfg, feishuBootstrapOptions{Name: "new", AppID: "new"})
		progress := cfg.Platforms["feishu"].Bots[1].Progress
		if progress == nil || progress.Mode != "stream" {
			t.Fatalf("new progress=%+v", progress)
		}
		upsertFeishuBotConfig(&cfg, feishuBootstrapOptions{Name: "explicit", AppID: "explicit", ProgressMode: "off"})
		if cfg.Platforms["feishu"].Bots[2].Progress.Mode != "off" {
			t.Fatal("explicit mode changed")
		}
	}
}

func TestFeishuSetupChecklistIsCompleteWithoutSecrets(t *testing.T) {
	out := captureStdout(t, func() {
		printFeishuBootstrapResult(feishuBootstrapOptions{Name: "test", AppID: "test", AppSecret: "secret-never-print"})
	})
	for _, want := range []string{"机器人能力", "长连接", "im.message.receive_v1", "card.action.trigger", "allowed_users", "发布", "凭据", "不代表", "cardkit:card:write"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "secret-never-print") {
		t.Fatal("secret leaked")
	}
}
