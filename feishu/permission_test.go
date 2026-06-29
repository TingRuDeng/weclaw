package feishu

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIsPermissionErrorCodeCoversKnownFeishuPermissionCodes(t *testing.T) {
	for _, code := range []int{99991400, 99991401, 99991663, 99991672, 99991670, 99991668} {
		if !IsPermissionErrorCode(code) {
			t.Fatalf("code=%d should be treated as permission error", code)
		}
	}
}

func TestIsPermissionErrorCodeRejectsNonPermissionCodes(t *testing.T) {
	for _, code := range []int{200400, 200610, 99991699, 0} {
		if IsPermissionErrorCode(code) {
			t.Fatalf("code=%d should not be treated as permission error", code)
		}
	}
}

func TestIsPermissionErrorUsesCodeAndTextFallback(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "known code", err: formatFeishuAPIError("cli_a", 99991663, "missing scope"), want: true},
		{name: "non permission code", err: formatFeishuAPIError("cli_a", 200400, "bad request"), want: false},
		{name: "english fallback", err: errors.New("forbidden: no access to scope im:resource"), want: true},
		{name: "chinese fallback", err: errors.New("权限不足，请开通权限"), want: true},
		{name: "plain error", err: errors.New("network timeout"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPermissionError(tt.err); got != tt.want {
				t.Fatalf("IsPermissionError(%v)=%v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestPermissionGuideMessageIncludesAppPermissionURL(t *testing.T) {
	msg := PermissionGuideMessage("cli_a")

	if !strings.Contains(msg, "https://open.feishu.cn/app/cli_a/permission") {
		t.Fatalf("message=%q, want permission url", msg)
	}
}

func TestBuildPermissionGuideTextIncludesScopesAndNoSecret(t *testing.T) {
	msg := buildPermissionGuideText("cli_a", true)

	for _, want := range []string{
		"https://open.feishu.cn/app/cli_a/permission",
		"im:message",
		"im:message:send_as_bot",
		"im:resource",
		"im:chat",
		"cardkit:card",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message=%q, want %q", msg, want)
		}
	}
	if strings.Contains(strings.ToLower(msg), "secret") {
		t.Fatalf("message leaks secret-like text: %q", msg)
	}
}

func TestBuildPermissionGuideCardIncludesScopes(t *testing.T) {
	cardJSON, err := buildPermissionGuideCard("cli_a", true)
	if err != nil {
		t.Fatalf("buildPermissionGuideCard error: %v", err)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("card json invalid: %v", err)
	}
	if card["schema"] != "2.0" {
		t.Fatalf("schema=%#v, want CardKit 2.0", card["schema"])
	}
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	markdown := elements[0].(map[string]any)
	content := markdown["content"].(string)
	if !strings.Contains(content, "im:resource") || !strings.Contains(content, "cardkit:card") {
		t.Fatalf("card content=%q, want permission scopes", content)
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
