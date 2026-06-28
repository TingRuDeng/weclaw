package platform

import "testing"

func TestIncomingMessageConversationKey(t *testing.T) {
	msg := IncomingMessage{
		Platform: PlatformWeChat,
		UserID:   "user-1",
	}

	if got := msg.ConversationKey(); got != "wechat:user-1" {
		t.Fatalf("ConversationKey=%q, want wechat:user-1", got)
	}
}

func TestIncomingMessageConversationKeyTrimsValues(t *testing.T) {
	msg := IncomingMessage{
		Platform: " feishu ",
		UserID:   " open-id-1 ",
	}

	if got := msg.ConversationKey(); got != "feishu:open-id-1" {
		t.Fatalf("ConversationKey=%q, want feishu:open-id-1", got)
	}
}

func TestAttachmentKindConstants(t *testing.T) {
	kinds := []AttachmentKind{
		AttachmentImage,
		AttachmentFile,
		AttachmentAudio,
		AttachmentVideo,
	}

	for _, kind := range kinds {
		if kind == "" {
			t.Fatalf("attachment kind should not be empty")
		}
	}
}
