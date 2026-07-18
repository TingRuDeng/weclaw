package feishu

import (
	"context"
	"testing"
)

func TestSDKMessageSenderPatchCardUsesOriginalMessage(t *testing.T) {
	var gotMessageID, gotContent string
	sender := &sdkMessageSender{patch: func(_ context.Context, messageID string, content string) (int, string, error) {
		gotMessageID, gotContent = messageID, content
		return 0, "", nil
	}}
	if err := sender.PatchCard(context.Background(), "om_card", `{"schema":"2.0"}`); err != nil {
		t.Fatal(err)
	}
	if gotMessageID != "om_card" || gotContent != `{"schema":"2.0"}` {
		t.Fatalf("patch=(%q,%q)", gotMessageID, gotContent)
	}
}
