package feishu

import (
	"strings"
	"testing"
	"time"
)

func TestPermissionGuideMessageIncludesAppPermissionURL(t *testing.T) {
	msg := PermissionGuideMessage("cli_a")

	if !strings.Contains(msg, "https://open.feishu.cn/app/cli_a/permission") {
		t.Fatalf("message=%q, want permission url", msg)
	}
}

func TestPermissionGuideLimiterCoolsDownRepeatedMessages(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	limiter := newPermissionGuideLimiter("cli_a")
	limiter.now = func() time.Time { return now }

	if _, ok := limiter.MessageForCode(99991400); !ok {
		t.Fatal("first permission error should emit guide")
	}
	if _, ok := limiter.MessageForCode(99991400); ok {
		t.Fatal("second permission error within cooldown should be suppressed")
	}
	now = now.Add(permissionGuideCooldown)
	if _, ok := limiter.MessageForCode(99991400); !ok {
		t.Fatal("permission error after cooldown should emit guide")
	}
}

func TestFormatFeishuAPIErrorDoesNotExposeSecret(t *testing.T) {
	err := formatFeishuAPIError("cli_a", 99991400, "permission denied")

	if err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("error=%v, want permission guide", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaks secret: %v", err)
	}
}
