package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestRunUsersApproveCodeWritesWechatAllowedUser(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { usersApproveAdmin = false })
	writeUsersAccessCodeForTest(t, time.Now().Add(30*time.Minute))
	cfg := config.DefaultConfig()
	cfg.Platforms["wechat"] = config.PlatformConfig{}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	output := captureStdout(t, func() {
		if err := runUsersApproveCodeForTest("123456", true); err != nil {
			t.Fatalf("approve code: %v", err)
		}
	})

	if !strings.Contains(output, "已授权 wechat 用户: wx_user@im.wechat") ||
		!strings.Contains(output, "已同步加入 admin_users") {
		t.Fatalf("output=%q, want approve result", output)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !usersTestStringSliceContains(loaded.Platforms["wechat"].AllowedUsers, "wx_user@im.wechat") {
		t.Fatalf("allowed_users=%#v, want user", loaded.Platforms["wechat"].AllowedUsers)
	}
}

func runUsersApproveCodeForTest(code string, admin bool) error {
	usersApproveAdmin = admin
	return usersApproveCodeCmd.RunE(usersApproveCodeCmd, []string{code})
}

func writeUsersAccessCodeForTest(t *testing.T, expiresAt time.Time) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".weclaw", "access-codes.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir access code dir: %v", err)
	}
	data := `{
  "version": 1,
  "records": {
    "123456": {
      "code": "123456",
      "platform": "wechat",
      "user_id": "wx_user@im.wechat",
      "expires_at": "` + expiresAt.UTC().Format(time.RFC3339) + `"
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write access code state: %v", err)
	}
}

func usersTestStringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
