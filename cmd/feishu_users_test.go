package cmd

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestRunFeishuUsersPendingPrintsDiscoveredIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuBotUserForTest(t, "cli_b", "on_approved")

	output := captureStdout(t, func() {
		if err := runFeishuUsers("pending"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})

	if !strings.Contains(output, "待确认飞书用户") ||
		!strings.Contains(output, "on_same_person") ||
		!strings.Contains(output, "cli_a") {
		t.Fatalf("output=%q, want pending identity", output)
	}
	if strings.Contains(output, "on_approved") {
		t.Fatalf("output=%q, should hide approved identity from pending", output)
	}
	if !strings.Contains(output, "卡片管家 (project-a, cli_a)") {
		t.Fatalf("output=%q, want readable bot label", output)
	}
	if !strings.Contains(output, "授权命令: weclaw feishu users approve on_same_person") ||
		!strings.Contains(output, "授权说明: 执行上面的授权命令可授权该用户访问待授权机器人。") ||
		!strings.Contains(output, "管理员命令: weclaw feishu users approve on_same_person --admin") {
		t.Fatalf("output=%q, want approve command hints", output)
	}
}

func TestRunFeishuUsersListPrintsOnlyApprovedIdentities(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuUserForTest(t, "on_approved")

	output := captureStdout(t, func() {
		if err := runFeishuUsers("list"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})

	if !strings.Contains(output, "已授权飞书用户") ||
		!strings.Contains(output, "on_approved") {
		t.Fatalf("output=%q, want approved identity", output)
	}
	if strings.Contains(output, "on_same_person") {
		t.Fatalf("output=%q, should hide pending identity from list", output)
	}
}

func TestRunFeishuUsersListPrintsAuthorizedScopeWithoutAuthCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateWithApprovedAuthCodeForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuBotUserForTest(t, "cli_a", "on_same_person")

	output := captureStdout(t, func() {
		if err := runFeishuUsers("list"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})

	if !strings.Contains(output, "on_same_person") ||
		!strings.Contains(output, "已授权机器人: 卡片管家") ||
		!strings.Contains(output, "状态: 已授权") {
		t.Fatalf("output=%q, want authorized bot scope only", output)
	}
	if strings.Contains(output, "授权码: 123456") ||
		strings.Contains(output, "approve-code 123456") ||
		strings.Contains(output, "待授权机器人") ||
		strings.Contains(output, "下一步: weclaw feishu users pending") ||
		strings.Contains(output, "部分授权") {
		t.Fatalf("output=%q, list should not print pending scope", output)
	}
}

func TestRunFeishuUsersPendingPrintsAuthCodeCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateWithAuthCodeForTest(t)
	writeFeishuBotsConfigForTest(t)

	output := captureStdout(t, func() {
		if err := runFeishuUsers("pending"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})

	if !strings.Contains(output, "授权码: 123456") ||
		!strings.Contains(output, "weclaw feishu users approve-code 123456") ||
		!strings.Contains(output, "weclaw feishu users approve-code 123456 --admin") {
		t.Fatalf("output=%q, want approve-code command hints", output)
	}
}

func TestRunFeishuUsersPendingPrintsApprovedIdentityWithAuthCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateWithApprovedAuthCodeForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuBotUserForTest(t, "cli_a", "on_same_person")

	output := captureStdout(t, func() {
		if err := runFeishuUsers("pending"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})

	if !strings.Contains(output, "授权码: 123456") ||
		!strings.Contains(output, "状态: 待确认") {
		t.Fatalf("output=%q, want pending approved identity with active auth code", output)
	}
	if strings.Contains(output, "已授权机器人") {
		t.Fatalf("output=%q, pending should not print authorized scope", output)
	}
}

func TestRunFeishuUsersPendingPrintsUnauthorizedScopeWithoutAuthCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateWithApprovedExpiredAuthCodeForTest(t)
	writeFeishuBotsConfigForTest(t)
	authorizeFeishuBotUserForTest(t, "cli_a", "on_same_person")

	output := captureStdout(t, func() {
		if err := runFeishuUsers("pending"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})

	if !strings.Contains(output, "待授权机器人: project-b") ||
		!strings.Contains(output, "状态: 待授权") ||
		!strings.Contains(output, "授权命令: weclaw feishu users approve on_same_person") ||
		!strings.Contains(output, "授权说明: 执行上面的授权命令可授权该用户访问待授权机器人。") {
		t.Fatalf("output=%q, want pending unauthorized scope", output)
	}
	if strings.Contains(output, "授权码: 123456") ||
		strings.Contains(output, "approve-code 123456") ||
		strings.Contains(output, "已授权机器人") {
		t.Fatalf("output=%q, should hide expired auth code", output)
	}
}

func TestRunFeishuUsersPendingHidesExpiredAuthCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateWithExpiredAuthCodeForTest(t)
	writeFeishuBotsConfigForTest(t)

	output := captureStdout(t, func() {
		if err := runFeishuUsers("pending"); err != nil {
			t.Fatalf("runFeishuUsers error: %v", err)
		}
	})

	if strings.Contains(output, "授权码: 123456") ||
		strings.Contains(output, "approve-code 123456") {
		t.Fatalf("output=%q, should hide expired auth code", output)
	}
	if !strings.Contains(output, "weclaw feishu users approve on_same_person") {
		t.Fatalf("output=%q, want stable-id approval hint", output)
	}
}

func TestRunFeishuUsersApproveAddsAdminUser(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)

	output := captureStdout(t, func() {
		err := runFeishuUsersApprove(feishuUsersApproveOptions{
			Selector: "on_same_person",
			Admin:    true,
		})
		if err != nil {
			t.Fatalf("runFeishuUsersApprove error: %v", err)
		}
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	if got := strings.Join(cfg.AdminUsers, ","); got != "on_same_person" {
		t.Fatalf("admin_users=%q, want on_same_person", got)
	}
	for _, bot := range cfg.Platforms["feishu"].Bots {
		if got := strings.Join(bot.AllowedUsers, ","); got != "on_same_person" {
			t.Fatalf("bot %s allowed_users=%q, want on_same_person", bot.Name, got)
		}
	}
	if !strings.Contains(output, "已同步加入 admin_users") {
		t.Fatalf("output=%q, want admin completion message", output)
	}
}

func TestRunFeishuUsersApproveAddsAdminForApprovedIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)

	err := runFeishuUsersApprove(feishuUsersApproveOptions{
		Selector: "on_approved",
		Admin:    true,
	})
	if err != nil {
		t.Fatalf("runFeishuUsersApprove error: %v", err)
	}
	cfg, loadErr := config.Load()
	if loadErr != nil {
		t.Fatalf("config.Load error: %v", loadErr)
	}
	if got := strings.Join(cfg.AdminUsers, ","); got != "on_approved" {
		t.Fatalf("admin_users=%q, want on_approved", got)
	}
}

func TestRunFeishuUsersApproveRejectsAdminWithoutUnionID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateWithoutUnionForTest(t)
	writeFeishuBotsConfigForTest(t)

	err := runFeishuUsersApprove(feishuUsersApproveOptions{
		Selector: "ou_only",
		Admin:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "缺少 union_id") {
		t.Fatalf("error=%v, want missing union_id", err)
	}
	cfg, loadErr := config.Load()
	if loadErr != nil {
		t.Fatalf("config.Load error: %v", loadErr)
	}
	if len(cfg.AdminUsers) != 0 {
		t.Fatalf("admin_users=%#v, want empty", cfg.AdminUsers)
	}
}
