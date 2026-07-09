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

func TestRunUsersListShowsWechatAllowedUsers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Platforms["wechat"] = config.PlatformConfig{
		AllowedUsers: []string{"wx_user@im.wechat", "wx_admin@im.wechat"},
	}
	cfg.AdminUsers = []string{"wx_admin@im.wechat"}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	output := captureStdout(t, func() {
		if err := runUsersList(); err != nil {
			t.Fatalf("list users: %v", err)
		}
	})

	for _, want := range []string{
		"已授权微信用户:",
		"wx_user@im.wechat",
		"用户类型: 普通用户",
		"wx_admin@im.wechat",
		"用户类型: 管理员",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output=%q, want %q", output, want)
		}
	}
}

func TestRunUsersPendingShowsWechatAccessCodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeUsersAccessCodeForTest(t, time.Now().Add(30*time.Minute))

	output := captureStdout(t, func() {
		if err := runUsersPending(); err != nil {
			t.Fatalf("pending users: %v", err)
		}
	})

	for _, want := range []string{
		"待授权微信用户:",
		"wx_user@im.wechat",
		"授权码: 123456",
		"授权命令: weclaw wechat users approve-code 123456",
		"管理员命令: weclaw wechat users approve-code 123456 --admin",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output=%q, want %q", output, want)
		}
	}
}

func TestWechatUsersCommandPath(t *testing.T) {
	output := commandHelpText(t, wechatCmd)
	for _, want := range []string{
		"管理微信平台",
		"users",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("wechat help=%q, want %q", output, want)
		}
	}

	usersOutput := commandHelpText(t, newWechatUsersCmd())
	for _, want := range []string{
		"管理微信用户授权",
		"list",
		"pending",
		"approve-code",
	} {
		if !strings.Contains(usersOutput, want) {
			t.Fatalf("wechat users help=%q, want %q", usersOutput, want)
		}
	}
}

func runUsersApproveCodeForTest(code string, admin bool) error {
	cmd := newWechatUsersApproveCodeCmd("weclaw wechat users")
	usersApproveAdmin = admin
	return cmd.RunE(cmd, []string{code})
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
