package cmd

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestRunFeishuUsersRevokeRemovesAllBotsAndAdmin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuUserForTest(t, "on_same_person")
	addFeishuAdminUserForTest(t, "on_same_person")

	output := captureStdout(t, func() {
		if err := runFeishuUsersRevoke(feishuUsersRevokeOptions{
			Selector: "on_same_person",
			Admin:    true,
		}); err != nil {
			t.Fatalf("runFeishuUsersRevoke error: %v", err)
		}
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	for _, bot := range cfg.Platforms["feishu"].Bots {
		if usersTestStringSliceContains(bot.AllowedUsers, "on_same_person") {
			t.Fatalf("bot %s allowed_users=%#v, should remove user", bot.Name, bot.AllowedUsers)
		}
	}
	if usersTestStringSliceContains(cfg.AdminUsers, "on_same_person") {
		t.Fatalf("admin_users=%#v, should remove user", cfg.AdminUsers)
	}
	if !strings.Contains(output, "已取消飞书用户授权: on_same_person") ||
		!strings.Contains(output, "已移除机器人授权") ||
		!strings.Contains(output, "已移出 admin_users") {
		t.Fatalf("output=%q, want revoke summary", output)
	}
}

func TestRunFeishuUsersRevokeRemovesSingleBotOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuUserForTest(t, "on_same_person")
	addFeishuAdminUserForTest(t, "on_same_person")

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
	if !usersTestStringSliceContains(cfg.AdminUsers, "on_same_person") {
		t.Fatalf("admin_users=%#v, should keep admin without --admin", cfg.AdminUsers)
	}
	if !strings.Contains(output, "卡片管家") || strings.Contains(output, "project-b") {
		t.Fatalf("output=%q, want only selected bot in summary", output)
	}
}

func addFeishuAdminUserForTest(t *testing.T, userID string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	cfg.AdminUsers = append(cfg.AdminUsers, userID)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}
}
