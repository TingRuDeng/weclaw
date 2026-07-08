package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestRunFeishuUsersPendingPrintsDiscoveredIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFeishuIdentityStateForTest(t)
	writeFeishuBotsConfigForTest(t)

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
	if !strings.Contains(output, "授权访问: weclaw feishu users approve on_same_person") ||
		!strings.Contains(output, "设为管理员: weclaw feishu users approve on_same_person --admin") {
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
		!strings.Contains(output, "待授权机器人: project-b") ||
		!strings.Contains(output, "状态: 部分授权") {
		t.Fatalf("output=%q, want authorized and pending bot scopes", output)
	}
	if strings.Contains(output, "授权码: 123456") ||
		strings.Contains(output, "approve-code 123456") {
		t.Fatalf("output=%q, list should not print auth code commands", output)
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
		!strings.Contains(output, "状态: 部分授权，待确认") {
		t.Fatalf("output=%q, want pending approved identity with active auth code", output)
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

func writeFeishuIdentityStateForTest(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".weclaw", "feishu-identities.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	data := `{
  "version": 1,
  "records": {
    "on_same_person": {
      "key": "on_same_person",
      "union_id": "on_same_person",
      "open_id": "ou_a",
      "open_ids": {"cli_a": "ou_a"},
      "accounts": ["cli_a"],
      "pending": true,
      "approved": false,
      "last_seen": "2026-07-08T00:00:00Z"
    },
    "on_approved": {
      "key": "on_approved",
      "union_id": "on_approved",
      "open_id": "ou_b",
      "open_ids": {"cli_b": "ou_b"},
      "accounts": ["cli_b"],
      "pending": false,
      "approved": true,
      "last_seen": "2026-07-08T00:00:01Z"
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write identity state: %v", err)
	}
}

func writeFeishuIdentityStateWithoutUnionForTest(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".weclaw", "feishu-identities.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	data := `{
  "version": 1,
  "records": {
    "ou_only": {
      "key": "ou_only",
      "open_id": "ou_only",
      "open_ids": {"cli_a": "ou_only"},
      "accounts": ["cli_a"],
      "pending": true,
      "approved": false,
      "last_seen": "2026-07-08T00:00:00Z"
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write identity state: %v", err)
	}
}

func writeFeishuIdentityStateWithAuthCodeForTest(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".weclaw", "feishu-identities.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	data := `{
  "version": 1,
  "records": {
    "on_same_person": {
      "key": "on_same_person",
      "union_id": "on_same_person",
      "open_id": "ou_a",
      "open_ids": {"cli_a": "ou_a"},
      "accounts": ["cli_a"],
      "auth_code": "123456",
      "auth_code_expires_at": "2099-01-01T00:00:00Z",
      "pending": true,
      "approved": false,
      "last_seen": "2026-07-08T00:00:00Z"
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write identity state: %v", err)
	}
}

func writeFeishuIdentityStateWithApprovedAuthCodeForTest(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".weclaw", "feishu-identities.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	data := `{
  "version": 1,
  "records": {
    "on_same_person": {
      "key": "on_same_person",
      "union_id": "on_same_person",
      "open_id": "ou_a",
      "open_ids": {"cli_a": "ou_a", "cli_b": "ou_b"},
      "accounts": ["cli_a", "cli_b"],
      "auth_code": "123456",
      "auth_code_expires_at": "2099-01-01T00:00:00Z",
      "pending": false,
      "approved": true,
      "last_seen": "2026-07-08T00:00:00Z"
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write identity state: %v", err)
	}
}

func writeFeishuIdentityStateWithExpiredAuthCodeForTest(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".weclaw", "feishu-identities.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	data := `{
  "version": 1,
  "records": {
    "on_same_person": {
      "key": "on_same_person",
      "union_id": "on_same_person",
      "open_id": "ou_a",
      "open_ids": {"cli_a": "ou_a"},
      "accounts": ["cli_a"],
      "auth_code": "123456",
      "auth_code_expires_at": "2000-01-01T00:00:00Z",
      "pending": true,
      "approved": false,
      "last_seen": "2026-07-08T00:00:00Z"
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write identity state: %v", err)
	}
}

func writeFeishuBotsConfigForTest(t *testing.T) {
	t.Helper()
	enabled := true
	cfg := config.DefaultConfig()
	cfg.Platforms["feishu"] = config.PlatformConfig{
		Enabled: &enabled,
		Bots: []config.FeishuBotConfig{
			{Name: "project-a", DisplayName: "卡片管家", AppID: "cli_a"},
			{Name: "project-b", AppID: "cli_b"},
		},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}
}

func authorizeFeishuUserForTest(t *testing.T, userID string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	feishuCfg := cfg.Platforms["feishu"]
	for i := range feishuCfg.Bots {
		feishuCfg.Bots[i].AllowedUsers = append(feishuCfg.Bots[i].AllowedUsers, userID)
	}
	cfg.Platforms["feishu"] = feishuCfg
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}
}

func authorizeFeishuBotUserForTest(t *testing.T, appID string, userID string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	feishuCfg := cfg.Platforms["feishu"]
	for i := range feishuCfg.Bots {
		if feishuCfg.Bots[i].AppID == appID {
			feishuCfg.Bots[i].AllowedUsers = append(feishuCfg.Bots[i].AllowedUsers, userID)
		}
	}
	cfg.Platforms["feishu"] = feishuCfg
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}
}
