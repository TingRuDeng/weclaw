package cmd

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestEnableWeChatPlatformPersistsExplicitSelection(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	enabled := true
	cfg := config.DefaultConfig()
	cfg.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{
		Enabled: &enabled,
		Bots:    []config.FeishuBotConfig{{Name: "main", AppID: "cli_main"}},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := enableWeChatPlatform(); err != nil {
		t.Fatalf("enableWeChatPlatform: %v", err)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wechat := loaded.Platforms[string(platform.PlatformWeChat)]
	if wechat.Enabled == nil || !*wechat.Enabled {
		t.Fatalf("wechat enabled=%v, want true", wechat.Enabled)
	}
}
