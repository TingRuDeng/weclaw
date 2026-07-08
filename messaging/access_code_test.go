package messaging

import (
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestObserveDeniedIdentityIssuesWechatAccessCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	handler := NewHandler(nil, nil)

	notice := handler.ObserveDeniedIdentity(platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "wx_user@im.wechat",
	})

	if !strings.Contains(notice, "当前账号无权限") || !strings.Contains(notice, "授权码: ") {
		t.Fatalf("notice=%q, want access code notice", notice)
	}
}

func TestApproveAccessCodeWritesWechatAllowedUserAndAdmin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Platforms[string(platform.PlatformWeChat)] = config.PlatformConfig{}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	record, ok := issueAccessCode(DefaultAccessCodeFile(), platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "wx_user@im.wechat",
	}, time.Now().UTC())
	if !ok {
		t.Fatal("issueAccessCode ok=false, want true")
	}

	result, err := ApproveAccessCode(AccessCodeApprovalRequest{Code: record.Code, Admin: true})
	if err != nil {
		t.Fatalf("ApproveAccessCode error: %v", err)
	}

	if result.Identity != "wx_user@im.wechat" || result.Platform != string(platform.PlatformWeChat) || !result.Admin {
		t.Fatalf("result=%#v, want approved wechat admin", result)
	}
	loaded, loadErr := config.Load()
	if loadErr != nil {
		t.Fatalf("load config: %v", loadErr)
	}
	if !stringSliceContains(loaded.Platforms[string(platform.PlatformWeChat)].AllowedUsers, "wx_user@im.wechat") {
		t.Fatalf("wechat allowed_users=%#v, want user", loaded.Platforms[string(platform.PlatformWeChat)].AllowedUsers)
	}
	if !stringSliceContains(loaded.AdminUsers, "wx_user@im.wechat") {
		t.Fatalf("admin_users=%#v, want user", loaded.AdminUsers)
	}
	if _, err := ApproveAccessCode(AccessCodeApprovalRequest{Code: record.Code}); err == nil {
		t.Fatal("used access code should be cleared")
	}
}
