package messaging

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestCodexSessionCommandAdminDoesNotReacceptFeishuOpenID(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"ou_legacy_admin"})

	if h.codexSessionCommandAdmin(codexSessionCommandRequest{
		Platform: platform.PlatformFeishu,
	}, "ou_legacy_admin") {
		t.Fatal("Feishu open_id in admin_users must not bypass the union_id-only boundary decision")
	}
	if !h.codexSessionCommandAdmin(codexSessionCommandRequest{
		Platform: platform.PlatformFeishu,
		Admin:    true,
	}, "ou_user") {
		t.Fatal("explicit Feishu union_id-based admin decision was not preserved")
	}
	if !h.codexSessionCommandAdmin(codexSessionCommandRequest{}, "ou_legacy_admin") {
		t.Fatal("non-platform internal callers should retain the existing admin_users fallback")
	}
}

func TestPlatformMessageLogTextRedactsApprovalCommandsWithExtraFields(t *testing.T) {
	tests := map[string]string{
		"/approve ABCD2345":         "/approve <redacted>",
		"/approve ABCD2345 ignored": "/approve <redacted>",
		"  /DeNy secret extra  ":    "/DeNy <redacted>",
	}
	for input, want := range tests {
		if got := platformMessageLogText(input); got != want {
			t.Fatalf("platformMessageLogText(%q)=%q, want %q", input, got, want)
		}
	}
}
