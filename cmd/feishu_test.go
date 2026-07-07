package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
)

func TestRunFeishuLoginValidatesBeforeSave(t *testing.T) {
	oldValidator := validateFeishuCreds
	oldSaver := saveFeishuCreds
	defer func() {
		validateFeishuCreds = oldValidator
		saveFeishuCreds = oldSaver
	}()
	var validated bool
	var saved bool
	validateFeishuCreds = func(ctx context.Context, creds feishu.Credentials) error {
		validated = true
		if creds.AppID != "cli_a" || creds.AppSecret != "secret-a" {
			t.Fatalf("creds=%#v, want input credentials", creds)
		}
		return nil
	}
	saveFeishuCreds = func(name string, creds feishu.Credentials) error {
		if !validated {
			t.Fatal("save called before validation")
		}
		if name != "project-a" {
			t.Fatalf("name=%q, want project-a", name)
		}
		saved = true
		return nil
	}

	if err := runFeishuLogin(context.Background(), "project-a", "cli_a", "secret-a"); err != nil {
		t.Fatalf("runFeishuLogin error: %v", err)
	}
	if !saved {
		t.Fatal("credentials were not saved")
	}
}

func TestRunFeishuStatusDoesNotPrintSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret := "secret-should-not-print"
	if err := feishu.SaveCredentialsForBot("project-a", feishu.Credentials{AppID: "cli_a", AppSecret: secret}); err != nil {
		t.Fatalf("SaveCredentialsForBot error: %v", err)
	}
	oldValidator := validateFeishuCreds
	defer func() { validateFeishuCreds = oldValidator }()
	validateFeishuCreds = func(ctx context.Context, creds feishu.Credentials) error {
		return nil
	}

	output := captureStdout(t, func() {
		if err := runFeishuStatus(context.Background(), "project-a"); err != nil {
			t.Fatalf("runFeishuStatus error: %v", err)
		}
	})

	if strings.Contains(output, secret) {
		t.Fatalf("status output leaks secret: %s", output)
	}
	if !strings.Contains(output, "飞书凭证有效") || !strings.Contains(output, "cli_a") {
		t.Fatalf("status output=%q, want valid status with app id", output)
	}
}

func TestRunFeishuBootstrapCreatesBotConfigAndCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restoreFeishuCredentialHooks(t)

	var saved feishu.Credentials
	validateFeishuCreds = func(ctx context.Context, creds feishu.Credentials) error {
		return nil
	}
	saveFeishuCreds = func(name string, creds feishu.Credentials) error {
		if name != "project-a" {
			t.Fatalf("name=%q, want project-a", name)
		}
		saved = creds
		return nil
	}

	output := captureStdout(t, func() {
		err := runFeishuBootstrap(context.Background(), feishuBootstrapOptions{
			Name:         "project-a",
			AppID:        "cli_a",
			AppSecret:    "secret-a",
			AllowedUsers: []string{"ou_1", "ou_2"},
			DefaultAgent: "codex",
			ProgressMode: "stream",
		})
		if err != nil {
			t.Fatalf("runFeishuBootstrap error: %v", err)
		}
	})

	if saved.AppID != "cli_a" || saved.AppSecret != "secret-a" {
		t.Fatalf("saved=%#v, want input credentials", saved)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	feishuCfg := cfg.Platforms["feishu"]
	if feishuCfg.Enabled == nil || !*feishuCfg.Enabled {
		t.Fatalf("feishu enabled=%#v, want true", feishuCfg.Enabled)
	}
	if len(feishuCfg.Bots) != 1 {
		t.Fatalf("bots=%#v, want one bot", feishuCfg.Bots)
	}
	bot := feishuCfg.Bots[0]
	if bot.Name != "project-a" || bot.AppID != "cli_a" {
		t.Fatalf("bot=%#v, want project-a/cli_a", bot)
	}
	if strings.Join(bot.AllowedUsers, ",") != "ou_1,ou_2" {
		t.Fatalf("allowed users=%#v", bot.AllowedUsers)
	}
	if bot.DefaultAgent != "codex" {
		t.Fatalf("default agent=%q, want codex", bot.DefaultAgent)
	}
	if bot.Progress == nil || bot.Progress.Mode != "stream" {
		t.Fatalf("progress=%#v, want stream", bot.Progress)
	}
	if strings.Contains(output, "secret-a") {
		t.Fatalf("bootstrap output leaks secret: %s", output)
	}
	if !strings.Contains(output, "飞书 bootstrap 完成") {
		t.Fatalf("output=%q, want completion message", output)
	}
}

func TestRunFeishuBootstrapUpdatesExistingBotByName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restoreFeishuCredentialHooks(t)
	enabled := true
	initial := config.DefaultConfig()
	initial.Platforms["feishu"] = config.PlatformConfig{
		Enabled: &enabled,
		Bots: []config.FeishuBotConfig{
			{
				Name:                  "project-a",
				AppID:                 "cli_old",
				AllowedUsers:          []string{"ou_old"},
				Progress:              &config.ProgressConfig{Mode: "summary"},
				RequireMentionInGroup: feishuBoolPtr(false),
			},
		},
	}
	if err := config.Save(initial); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}
	validateFeishuCreds = func(ctx context.Context, creds feishu.Credentials) error {
		return nil
	}
	saveFeishuCreds = func(name string, creds feishu.Credentials) error {
		return nil
	}

	if err := runFeishuBootstrap(context.Background(), feishuBootstrapOptions{
		Name:         "project-a",
		AppID:        "cli_new",
		AppSecret:    "secret-new",
		AllowedUsers: []string{"ou_new"},
		DefaultAgent: "claude",
	}); err != nil {
		t.Fatalf("runFeishuBootstrap error: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	bots := cfg.Platforms["feishu"].Bots
	if len(bots) != 1 {
		t.Fatalf("bots=%#v, want update in place", bots)
	}
	if bots[0].AppID != "cli_new" || bots[0].DefaultAgent != "claude" {
		t.Fatalf("bot=%#v, want updated app and agent", bots[0])
	}
	if strings.Join(bots[0].AllowedUsers, ",") != "ou_new" {
		t.Fatalf("allowed users=%#v, want ou_new", bots[0].AllowedUsers)
	}
	if bots[0].Progress == nil || bots[0].Progress.Mode != "summary" {
		t.Fatalf("progress=%#v, want existing summary preserved", bots[0].Progress)
	}
	if bots[0].RequireMentionInGroup == nil || *bots[0].RequireMentionInGroup {
		t.Fatalf("require mention=%#v, want existing false preserved", bots[0].RequireMentionInGroup)
	}
}

func feishuBoolPtr(value bool) *bool {
	return &value
}

func restoreFeishuCredentialHooks(t *testing.T) {
	t.Helper()
	oldValidator := validateFeishuCreds
	oldSaver := saveFeishuCreds
	t.Cleanup(func() {
		validateFeishuCreds = oldValidator
		saveFeishuCreds = oldSaver
	})
}

// captureStdout 捕获命令输出，确保状态命令不会打印敏感信息。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writeEnd
	defer func() { os.Stdout = oldStdout }()

	fn()
	if err := writeEnd.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, readEnd); err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return buf.String()
}
