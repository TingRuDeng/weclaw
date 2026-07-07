package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
)

func TestRunFeishuStatusResolvesDisplayName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restoreFeishuCredentialHooks(t)
	saveFeishuBotConfigForTest(t, config.FeishuBotConfig{
		Name:        "project-a",
		DisplayName: "卡片管家",
		AppID:       "cli_a",
		Aliases:     []string{"信用卡管理"},
	})
	if err := feishu.SaveCredentialsForBot("project-a", feishu.Credentials{AppID: "cli_a", AppSecret: "secret-a"}); err != nil {
		t.Fatalf("SaveCredentialsForBot error: %v", err)
	}
	validateFeishuCreds = func(ctx context.Context, creds feishu.Credentials) error {
		return nil
	}

	output := captureStdout(t, func() {
		if err := runFeishuStatus(context.Background(), "卡片管家"); err != nil {
			t.Fatalf("runFeishuStatus error: %v", err)
		}
	})

	if !strings.Contains(output, "Bot: project-a") {
		t.Fatalf("output=%q, want canonical bot name", output)
	}
	if !strings.Contains(output, "显示名：卡片管家") {
		t.Fatalf("output=%q, want display name", output)
	}
}

func TestResolveFeishuBotNameMatchesAlias(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveFeishuBotConfigForTest(t, config.FeishuBotConfig{
		Name:    "project-a",
		AppID:   "cli_a",
		Aliases: []string{"卡管"},
	})

	name, err := resolveFeishuBotName("卡管")

	if err != nil {
		t.Fatalf("resolveFeishuBotName error=%v", err)
	}
	if name != "project-a" {
		t.Fatalf("name=%q, want project-a", name)
	}
}

func saveFeishuBotConfigForTest(t *testing.T, bot config.FeishuBotConfig) {
	t.Helper()
	enabled := true
	cfg := config.DefaultConfig()
	cfg.Platforms["feishu"] = config.PlatformConfig{
		Enabled: &enabled,
		Bots:    []config.FeishuBotConfig{bot},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}
}
