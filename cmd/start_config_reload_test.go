package cmd

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/platform"
)

type configReloadTestPlatform struct {
	name    platform.PlatformName
	account string
}

func (p *configReloadTestPlatform) Name() platform.PlatformName { return p.name }
func (p *configReloadTestPlatform) AccountID() string           { return p.account }
func (p *configReloadTestPlatform) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true}
}
func (p *configReloadTestPlatform) Run(ctx context.Context, _ platform.DispatchFunc) error {
	<-ctx.Done()
	return nil
}

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

func TestApplySoftConfigDoesNotRestoreStalePlatformAccess(t *testing.T) {
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &configReloadTestPlatform{name: platform.PlatformFeishu, account: "cli_a"},
		Access:   platform.NewAccessControl(nil),
	}})
	stale := config.DefaultConfig()
	stale.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{Bots: []config.FeishuBotConfig{{
		Name: "main", AppID: "cli_a", AllowedUsers: []string{"victim"},
	}}}

	applySoftConfig(messaging.NewHandler(nil, nil), registry, stale)
	_, allowed := registry.AuthorizeIncomingMessage(platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "victim",
	})
	if allowed {
		t.Fatal("stale soft-config snapshot restored revoked platform access")
	}
}

func TestLatestPlatformAccessUpdaterIgnoresStaleCommittedSlice(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	latest := config.DefaultConfig()
	latest.Platforms[string(platform.PlatformFeishu)] = config.PlatformConfig{Bots: []config.FeishuBotConfig{{
		Name: "main", AppID: "cli_a",
	}}}
	if err := config.Save(latest); err != nil {
		t.Fatal(err)
	}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &configReloadTestPlatform{name: platform.PlatformFeishu, account: "cli_a"},
		Access:   platform.NewAccessControl([]string{"victim"}),
	}})

	latestPlatformAccessUpdater(registry)(platform.PlatformFeishu, "cli_a", []string{"victim"})
	_, allowed := registry.AuthorizeIncomingMessage(platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "victim",
	})
	if allowed {
		t.Fatal("runtime updater trusted stale committed slice instead of latest disk config")
	}
}

func TestApplyPlatformAccessConfigRevokesRemovedAccount(t *testing.T) {
	registry := platform.NewRegistry([]platform.RegistryEntry{
		{
			Platform: &configReloadTestPlatform{name: platform.PlatformFeishu, account: "cli_a"},
			Access:   platform.NewAccessControl([]string{"user-a"}),
		},
		{
			Platform: &configReloadTestPlatform{name: platform.PlatformFeishu, account: "cli_b"},
			Access:   platform.NewAccessControl([]string{"user-b"}),
		},
	})
	enabled := true
	applyPlatformAccessConfig(registry, map[string]config.PlatformConfig{
		string(platform.PlatformFeishu): {
			Enabled: &enabled,
			Bots: []config.FeishuBotConfig{{
				Name: "main", AppID: "cli_a", AllowedUsers: []string{"user-a"},
			}},
		},
	})

	if _, allowed := registry.AuthorizeIncomingMessage(platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_b", UserID: "user-b",
	}); allowed {
		t.Fatal("removed Feishu account retained its previous runtime authorization")
	}
}

func TestApplyPlatformAccessConfigDeniesDisabledPlatform(t *testing.T) {
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &configReloadTestPlatform{name: platform.PlatformFeishu, account: "cli_a"},
		Access:   platform.NewAccessControl([]string{"user-a"}),
	}})
	disabled := false
	applyPlatformAccessConfig(registry, map[string]config.PlatformConfig{
		string(platform.PlatformFeishu): {
			Enabled: &disabled,
			Bots: []config.FeishuBotConfig{{
				Name: "main", AppID: "cli_a", AllowedUsers: []string{"user-a"},
			}},
		},
	})

	if _, allowed := registry.AuthorizeIncomingMessage(platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "user-a",
	}); allowed {
		t.Fatal("disabled Feishu platform retained runtime authorization")
	}
}
