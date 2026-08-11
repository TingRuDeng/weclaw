package cmd

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestRunFeishuUsersRevokeRequiresBotWhenMultipleConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuUserForTest(t, "on_same_person")
	err := runFeishuUsersRevoke(feishuUsersRevokeOptions{Selector: "on_same_person"})
	if err == nil || !strings.Contains(err.Error(), "请使用 --bot") {
		t.Fatalf("error=%v, want explicit bot requirement", err)
	}
}

func TestRunFeishuUsersRevokeRemovesSingleBotOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuUserForTest(t, "on_same_person")

	output := captureStdout(t, func() {
		if err := runFeishuUsersRevoke(feishuUsersRevokeOptions{
			Selector: "on_same_person",
			BotRef:   "project-a",
		}); err != nil {
			t.Fatalf("runFeishuUsersRevoke error: %v", err)
		}
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	bots := cfg.Platforms["feishu"].Bots
	if usersTestStringSliceContains(bots[0].AllowedUsers, "on_same_person") {
		t.Fatalf("project-a allowed_users=%#v, should remove user", bots[0].AllowedUsers)
	}
	if !usersTestStringSliceContains(bots[1].AllowedUsers, "on_same_person") {
		t.Fatalf("project-b allowed_users=%#v, should keep user", bots[1].AllowedUsers)
	}
	if !strings.Contains(output, "卡片管家") || strings.Contains(output, "project-b") {
		t.Fatalf("output=%q, want only selected bot in summary", output)
	}
}
