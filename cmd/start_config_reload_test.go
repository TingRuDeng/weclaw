package cmd

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestSaveDefaultAgentPreservesLatestDiskConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	initial := config.DefaultConfig()
	initial.DefaultAgent = "claude"
	initial.RateLimitPerMinute = 10
	if err := config.Save(initial); err != nil {
		t.Fatal(err)
	}
	latest, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	latest.RateLimitPerMinute = 77
	if err := config.Save(latest); err != nil {
		t.Fatal(err)
	}

	if err := saveDefaultAgent("codex"); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultAgent != "codex" || got.RateLimitPerMinute != 77 {
		t.Fatalf("config=%#v, want default codex and latest rate limit", got)
	}
}

func TestExtractPlatformDefaultAgentsIncludesFeishuBots(t *testing.T) {
	defaults := extractPlatformDefaultAgents(map[string]config.PlatformConfig{
		string(platform.PlatformFeishu): {
			Bots: []config.FeishuBotConfig{
				{Name: "project-a", AppID: "cli_a", DefaultAgent: "codex"},
				{Name: "project-b", AppID: "cli_b", DefaultAgent: "claude"},
			},
		},
	})

	if got := defaults[messaging.PlatformAccountConfigKey(platform.PlatformFeishu, "cli_a")]; got != "codex" {
		t.Fatalf("cli_a default=%q, want codex", got)
	}
	if got := defaults[messaging.PlatformAccountConfigKey(platform.PlatformFeishu, "cli_b")]; got != "claude" {
		t.Fatalf("cli_b default=%q, want claude", got)
	}
}

func TestExtractPlatformProgressConfigsIncludesFeishuBots(t *testing.T) {
	progress := extractPlatformProgressConfigs(map[string]config.PlatformConfig{
		string(platform.PlatformFeishu): {
			Bots: []config.FeishuBotConfig{
				{Name: "project-a", AppID: "cli_a", Progress: &config.ProgressConfig{Mode: "stream"}},
			},
		},
	})

	key := messaging.PlatformAccountConfigKey(platform.PlatformFeishu, "cli_a")
	if got := progress[key].Mode; got != "stream" {
		t.Fatalf("cli_a progress mode=%q, want stream", got)
	}
}
