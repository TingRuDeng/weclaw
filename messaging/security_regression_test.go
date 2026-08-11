package messaging

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestCodexSessionCommandAdminUsesOnlyProjectedCapability(t *testing.T) {
	h := NewHandler(nil, nil)

	if h.codexSessionCommandAdmin(codexSessionCommandRequest{
		Platform: platform.PlatformFeishu,
	}, "ou_legacy_admin") {
		t.Fatal("actor identity alone must not create management access")
	}
	if !h.codexSessionCommandAdmin(codexSessionCommandRequest{
		Platform: platform.PlatformFeishu,
		Admin:    true,
	}, "ou_user") {
		t.Fatal("explicit Registry capability projection was not preserved")
	}
	if h.codexSessionCommandAdmin(codexSessionCommandRequest{}, "ou_legacy_admin") {
		t.Fatal("internal callers must not fall back to legacy admin_users")
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
