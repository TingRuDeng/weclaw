package wechat

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestIncomingFromWeixinText(t *testing.T) {
	msg := ilink.WeixinMessage{
		MessageID:    42,
		FromUserID:   "user-1",
		ToUserID:     "bot-1",
		ContextToken: "ctx-1",
		ItemList: []ilink.MessageItem{{
			Type:     ilink.ItemTypeText,
			TextItem: &ilink.TextItem{Text: "hello"},
		}},
	}

	got := IncomingFromWeixin(msg)

	if got.Platform != platform.PlatformWeChat || got.AccountID != "bot-1" || got.UserID != "user-1" || got.ChatID != "user-1" {
		t.Fatalf("unexpected routing fields: %#v", got)
	}
	if got.MessageID != "42" || got.Text != "hello" || got.ContextToken != "ctx-1" {
		t.Fatalf("unexpected content fields: %#v", got)
	}
	if got.ConversationKey() != "wechat:user-1" {
		t.Fatalf("ConversationKey=%q, want wechat:user-1", got.ConversationKey())
	}
}

func TestShouldDispatchWeixinMessageFiltersSelfEcho(t *testing.T) {
	tests := []struct {
		name string
		msg  ilink.WeixinMessage
		want bool
	}{
		{
			name: "client_id echo",
			msg:  ilink.WeixinMessage{ClientID: "weclaw:abc", MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish},
			want: false,
		},
		{
			name: "bot prefix echo",
			msg: ilink.WeixinMessage{
				FromUserID:   "bot-1",
				MessageType:  ilink.MessageTypeBot,
				MessageState: ilink.MessageStateFinish,
				ItemList:     []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "[WeClaw] 已发送"}}},
			},
			want: false,
		},
		{
			name: "normal user",
			msg:  ilink.WeixinMessage{MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldDispatchWeixinMessage("bot-1", tt.msg)
			if got != tt.want {
				t.Fatalf("ShouldDispatchWeixinMessage=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestIncomingFromWeixinVoiceUsesTranscription(t *testing.T) {
	msg := ilink.WeixinMessage{
		MessageID:  43,
		FromUserID: "user-1",
		ToUserID:   "bot-1",
		ItemList: []ilink.MessageItem{{
			Type:      ilink.ItemTypeVoice,
			VoiceItem: &ilink.VoiceItem{Text: "语音文本"},
		}},
	}

	got := IncomingFromWeixin(msg)

	if got.Text != "语音文本" {
		t.Fatalf("Text=%q, want 语音文本", got.Text)
	}
}

func TestIncomingFromWeixinAttachments(t *testing.T) {
	msg := ilink.WeixinMessage{
		MessageID:  44,
		FromUserID: "user-1",
		ToUserID:   "bot-1",
		ItemList: []ilink.MessageItem{
			{
				Type:      ilink.ItemTypeImage,
				ImageItem: &ilink.ImageItem{URL: "https://example.test/a.png"},
			},
			{
				Type:     ilink.ItemTypeFile,
				FileItem: &ilink.FileItem{FileName: "report.pdf", Len: "123"},
			},
		},
	}

	got := IncomingFromWeixin(msg)

	if len(got.Attachments) != 2 {
		t.Fatalf("attachments=%#v, want 2", got.Attachments)
	}
	if got.Attachments[0].Kind != platform.AttachmentImage || got.Attachments[0].SourceID != "https://example.test/a.png" {
		t.Fatalf("image attachment=%#v", got.Attachments[0])
	}
	if got.Attachments[1].Kind != platform.AttachmentFile || got.Attachments[1].FileName != "report.pdf" || got.Attachments[1].SizeBytes != 123 {
		t.Fatalf("file attachment=%#v", got.Attachments[1])
	}
}
