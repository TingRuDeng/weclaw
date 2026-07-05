package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestWechatEnabledDefaultsToTrue(t *testing.T) {
	cfg := config.DefaultConfig()

	if !wechatEnabled(cfg) {
		t.Fatal("wechat should be enabled when platforms.wechat.enabled is omitted")
	}
}

func TestWechatEnabledCanBeDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	disabled := false
	cfg.Platforms[string(platform.PlatformWeChat)] = config.PlatformConfig{Enabled: &disabled}

	if wechatEnabled(cfg) {
		t.Fatal("wechat should be disabled when platforms.wechat.enabled=false")
	}
}

func TestWechatAggregationWindowDefaultsAndDisables(t *testing.T) {
	if got := wechatAggregationWindow(config.PlatformConfig{}); got != 800*time.Millisecond {
		t.Fatalf("default aggregation window=%s, want 800ms", got)
	}
	zero := 0
	if got := wechatAggregationWindow(config.PlatformConfig{MessageAggregationMs: &zero}); got != 0 {
		t.Fatalf("disabled aggregation window=%s, want 0", got)
	}
}

func TestBuildPlatformRegistryRequiresFeishuCredentialsWhenEnabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	enabled := true
	disabled := false
	cfg.Platforms[string(platform.PlatformWeChat)] = config.PlatformConfig{Enabled: &disabled}
	cfg.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{Enabled: &enabled}

	_, err := buildPlatformRegistry(nil, cfg)

	if err == nil || !strings.Contains(err.Error(), "load feishu credentials") {
		t.Fatalf("buildPlatformRegistry error=%v, want feishu credential error", err)
	}
}
