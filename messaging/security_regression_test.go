package messaging

import (
	"strings"
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

func TestIncomingMessageLogSummaryContainsOnlyMetadata(t *testing.T) {
	secret := "/approve ABCD2345 ignored"
	got := traceSummaryForIncoming(platform.IncomingMessage{
		Attachments: []platform.Attachment{{Kind: platform.AttachmentFile}},
	}, secret)

	if strings.Contains(got, secret) || strings.Contains(got, "ABCD2345") {
		t.Fatalf("trace summary contains message body: %q", got)
	}
	for _, want := range []string{"text_runes=25", "attachments=1", "card_action=false"} {
		if !strings.Contains(got, want) {
			t.Fatalf("trace summary=%q, want %q", got, want)
		}
	}
}
