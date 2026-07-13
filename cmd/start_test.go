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
	err := persistDetectedStartConfig(true, config.DefaultConfig(), func(*config.Config) error {
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

func TestWechatEnabledDefaultsToTrue(t *testing.T) {
	cfg := config.DefaultConfig()

	if !wechatEnabled(cfg) {
		t.Fatal("wechat should be enabled when platforms.wechat.enabled is omitted")
	}
}

func TestWechatEnabledDefaultsToFalseWhenFeishuEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	enabled := true
	cfg.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{
		Enabled: &enabled,
		Bots: []config.FeishuBotConfig{
			{Name: "project-a", AppID: "cli_a"},
		},
	}

	if wechatEnabled(cfg) {
		t.Fatal("wechat should be disabled by default when feishu is enabled")
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

	if !wechatEnabled(cfg) {
		t.Fatal("wechat should stay enabled when explicitly configured with feishu")
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
