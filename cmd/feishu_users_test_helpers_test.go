package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func writeFeishuIdentityStateForTest(t *testing.T) {
	t.Helper()
	writeFeishuIdentityStateJSON(t, `{
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
}`)
}

func writeFeishuIdentityStateWithoutUnionForTest(t *testing.T) {
	t.Helper()
	writeFeishuIdentityStateJSON(t, `{
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
}`)
}

func writeFeishuIdentityStateWithAuthCodeForTest(t *testing.T) {
	t.Helper()
	writeFeishuIdentityStateJSON(t, `{
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
}`)
}

func writeFeishuIdentityStateWithApprovedAuthCodeForTest(t *testing.T) {
	t.Helper()
	writeFeishuIdentityStateJSON(t, feishuApprovedIdentityStateJSON("2099-01-01T00:00:00Z"))
}

func writeFeishuIdentityStateWithApprovedExpiredAuthCodeForTest(t *testing.T) {
	t.Helper()
	writeFeishuIdentityStateJSON(t, feishuApprovedIdentityStateJSON("2000-01-01T00:00:00Z"))
}

func feishuApprovedIdentityStateJSON(expiresAt string) string {
	return `{
  "version": 1,
  "records": {
    "on_same_person": {
      "key": "on_same_person",
      "union_id": "on_same_person",
      "open_id": "ou_a",
      "open_ids": {"cli_a": "ou_a", "cli_b": "ou_b"},
      "accounts": ["cli_a", "cli_b"],
      "auth_code": "123456",
      "auth_code_expires_at": "` + expiresAt + `",
      "pending": false,
      "approved": true,
      "last_seen": "2026-07-08T00:00:00Z"
    }
  }
}`
}

func writeFeishuIdentityStateWithExpiredAuthCodeForTest(t *testing.T) {
	t.Helper()
	writeFeishuIdentityStateJSON(t, `{
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
}`)
}

func writeFeishuIdentityStateJSON(t *testing.T, data string) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".weclaw", "feishu-identities.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
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
