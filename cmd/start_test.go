package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/platform"
)

// TestPersistDetectedStartConfigExposesSaveFailure 验证自动探测结果无法持久化时阻止启动。
func TestPersistDetectedStartConfigExposesSaveFailure(t *testing.T) {
	wantErr := errors.New("只读配置")
	err := persistDetectedStartConfig(true, config.DefaultConfig(), func(func(*config.Config) error) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("persistDetectedStartConfig error=%v, want %v", err, wantErr)
	}
}

// TestPrepareStartUsesOneValidatedConfigSnapshot 验证启动闭包不会在执行时重新加载配置。
func TestPrepareStartUsesOneValidatedConfigSnapshot(t *testing.T) {
	wantCfg := config.DefaultConfig()
	loads := 0
	preflights := 0
	prepared, err := prepareStart(context.Background(), startPreparationOps{
		loadConfig: func() (*config.Config, error) { loads++; return wantCfg, nil },
		preflight:  func(context.Context, *config.Config) error { preflights++; return nil },
		start: func(got *config.Config) error {
			if got != wantCfg {
				t.Fatal("启动闭包未使用已预检配置快照")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("prepareStart error: %v", err)
	}
	if err := prepared.run(); err != nil {
		t.Fatalf("prepared.run error: %v", err)
	}
	if loads != 1 || preflights != 1 {
		t.Fatalf("loads=%d preflights=%d, want 1/1", loads, preflights)
	}
}

func TestLoadStartConfigInitializesAPIToken(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())

	cfg, err := loadStartConfig()
	if err != nil {
		t.Fatalf("loadStartConfig: %v", err)
	}
	if strings.TrimSpace(cfg.APIToken) == "" {
		t.Fatal("loadStartConfig returned empty api_token")
	}
}

func TestPlatformSelectionLeavesFreshConfigUnselected(t *testing.T) {
	cfg := config.DefaultConfig()

	selection := resolvePlatformSelection(cfg, 0)
	if selection.wechat || selection.feishu {
		t.Fatalf("selection=%+v, want no platform selected", selection)
	}
}

func TestPlatformSelectionKeepsLegacyWeChatWithStoredAccount(t *testing.T) {
	cfg := config.DefaultConfig()

	selection := resolvePlatformSelection(cfg, 1)
	if !selection.wechat || selection.feishu {
		t.Fatalf("selection=%+v, want legacy WeChat only", selection)
	}
}

func TestPersistLegacyWeChatSelectionWritesExplicitEnabledFlag(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := persistLegacyWeChatSelection(cfg, 1, config.Update); err != nil {
		t.Fatalf("persistLegacyWeChatSelection: %v", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wechat := loaded.Platforms[string(platform.PlatformWeChat)]
	if wechat.Enabled == nil || !*wechat.Enabled {
		t.Fatalf("persisted wechat enabled=%v, want true", wechat.Enabled)
	}
}

func TestPersistLegacyWeChatSelectionPreservesNewerExplicitChoice(t *testing.T) {
	cfg := config.DefaultConfig()
	disabled := false
	if err := persistLegacyWeChatSelection(cfg, 1, func(mutate func(*config.Config) error) error {
		latest := config.DefaultConfig()
		latest.Platforms[string(platform.PlatformWeChat)] = config.PlatformConfig{Enabled: &disabled}
		return mutate(latest)
	}); err != nil {
		t.Fatalf("persistLegacyWeChatSelection: %v", err)
	}

	wechat := cfg.Platforms[string(platform.PlatformWeChat)]
	if wechat.Enabled == nil || *wechat.Enabled {
		t.Fatalf("wechat enabled=%v, want preserved explicit false", wechat.Enabled)
	}
	selection := resolvePlatformSelection(cfg, 1)
	if selection.wechat || selection.feishu {
		t.Fatalf("selection=%+v, want newer explicit choice", selection)
	}
}

func TestPlatformSelectionDefaultsToFeishuOnlyWhenEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	enabled := true
	cfg.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{
		Enabled: &enabled,
		Bots: []config.FeishuBotConfig{
			{Name: "project-a", AppID: "cli_a"},
		},
	}

	selection := resolvePlatformSelection(cfg, 1)
	if selection.wechat || !selection.feishu {
		t.Fatalf("selection=%+v, want Feishu only", selection)
	}
}

func TestWechatEnabledCanBeExplicitlyEnabledWithFeishu(t *testing.T) {
	cfg := config.DefaultConfig()
	enabled := true
	cfg.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{
		Enabled: &enabled,
		Bots: []config.FeishuBotConfig{
			{Name: "project-a", AppID: "cli_a"},
		},
	}
	cfg.Platforms[string(platform.PlatformWeChat)] = config.PlatformConfig{Enabled: &enabled}

	selection := resolvePlatformSelection(cfg, 1)
	if !selection.wechat || !selection.feishu {
		t.Fatalf("selection=%+v, want both platforms", selection)
	}
}

func TestWechatEnabledCanBeDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	disabled := false
	cfg.Platforms[string(platform.PlatformWeChat)] = config.PlatformConfig{Enabled: &disabled}

	selection := resolvePlatformSelection(cfg, 1)
	if selection.wechat || selection.feishu {
		t.Fatalf("selection=%+v, want both platforms disabled", selection)
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
	cfg.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{
		Enabled: &enabled,
		Bots: []config.FeishuBotConfig{
			{Name: "project-a", AppID: "cli_a", AllowedUsers: []string{"ou_1"}},
		},
	}

	_, err := buildPlatformRegistry(nil, cfg)

	if err == nil || !strings.Contains(err.Error(), "load feishu credentials") {
		t.Fatalf("buildPlatformRegistry error=%v, want feishu credential error", err)
	}
}

func TestBuildPlatformRegistryCreatesAllFeishuBots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := feishu.SaveCredentialsForBot("project-a", feishu.Credentials{AppID: "cli_a", AppSecret: "secret-a"}); err != nil {
		t.Fatalf("SaveCredentialsForBot project-a error: %v", err)
	}
	if err := feishu.SaveCredentialsForBot("project-b", feishu.Credentials{AppID: "cli_b", AppSecret: "secret-b"}); err != nil {
		t.Fatalf("SaveCredentialsForBot project-b error: %v", err)
	}
	cfg := config.DefaultConfig()
	enabled := true
	disabled := false
	cfg.Platforms[string(platform.PlatformWeChat)] = config.PlatformConfig{Enabled: &disabled}
	cfg.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{
		Enabled: &enabled,
		Bots: []config.FeishuBotConfig{
			{Name: "project-a", AppID: "cli_a", AllowedUsers: []string{"ou_a"}},
			{Name: "project-b", AppID: "cli_b", AllowedUsers: []string{"ou_b"}},
		},
	}

	registry, err := buildPlatformRegistry(nil, cfg)
	if err != nil {
		t.Fatalf("buildPlatformRegistry error: %v", err)
	}
	if _, ok := registry.ReplierFor(platform.PlatformFeishu, "cli_a", "oc_a"); !ok {
		t.Fatalf("missing replier for cli_a")
	}
	if _, ok := registry.ReplierFor(platform.PlatformFeishu, "cli_b", "oc_b"); !ok {
		t.Fatalf("missing replier for cli_b")
	}
}
