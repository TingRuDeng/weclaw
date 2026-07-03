package feishu

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type fakeResourceDownloader struct {
	attachments []platform.Attachment
	seen        []types.Resource
}

// DownloadResource 记录资源下载请求，并返回预设附件。
func (f *fakeResourceDownloader) DownloadResource(ctx context.Context, messageID string, resource types.Resource) (platform.Attachment, error) {
	f.seen = append(f.seen, resource)
	if len(f.attachments) == 0 {
		return platform.Attachment{Kind: platform.AttachmentFile, Path: "/tmp/file", SourceID: resource.FileKey}, nil
	}
	attachment := f.attachments[0]
	f.attachments = f.attachments[1:]
	return attachment, nil
}

func TestToIncomingFromMessageParsesText(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = &fakeResourceDownloader{}
	event := newMessageEvent("p2p", "text", `{"text":"<p>hello&nbsp; world</p><br>next"}`)

	incoming, ok := adapter.toIncomingFromMessage(context.Background(), event)

	if !ok {
		t.Fatal("toIncomingFromMessage ok=false, want true")
	}
	if incoming.Platform != platform.PlatformFeishu || incoming.UserID != "ou_user" || incoming.MessageID != "om_1" {
		t.Fatalf("incoming=%#v, want feishu user/message fields", incoming)
	}
	if incoming.Text != "hello  world\nnext" {
		t.Fatalf("text=%q, want cleaned text", incoming.Text)
	}
	if incoming.Metadata["feishu_session_key"] != "feishu:tenant_1:dm:oc_1:ou_user" {
		t.Fatalf("metadata=%#v, want scoped DM session", incoming.Metadata)
	}
	if incoming.Metadata["original_user_id"] != "ou_user" {
		t.Fatalf("metadata=%#v, want original_user_id=ou_user", incoming.Metadata)
	}
}

func TestToIncomingFromMessageIgnoresUnmentionedGroupByDefault(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := newMessageEvent("group", "text", `{"text":"hello"}`)

	_, ok := adapter.toIncomingFromMessage(context.Background(), event)

	if ok {
		t.Fatal("group chat should be ignored")
	}
}

func TestToIncomingFromMessageDispatchesMentionedGroup(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := newMessageEvent("group", "text", `{"text":"<at user_id=\"cli_a\">bot</at> hello"}`)
	event.Event.Message.RootId = stringPtr("om_root")
	event.Event.Message.Mentions = []*larkim.MentionEvent{newMention("cli_a")}

	incoming, ok := adapter.toIncomingFromMessage(context.Background(), event)

	if !ok {
		t.Fatal("mentioned group message should be dispatchable")
	}
	if incoming.UserID != "ou_user" {
		t.Fatalf("incoming.UserID=%q, want sender open_id for access control", incoming.UserID)
	}
	if incoming.Metadata["feishu_session_key"] != "feishu:tenant_1:group:oc_1:om_root" {
		t.Fatalf("metadata=%#v, want scoped group thread session", incoming.Metadata)
	}
	if incoming.Metadata["feishu_is_mentioned"] != "true" {
		t.Fatalf("metadata=%#v, want feishu_is_mentioned=true", incoming.Metadata)
	}
}

func TestIsMentionedBotUsesNormalizedFlag(t *testing.T) {
	if !isMentionedBotFromParts(feishuMentionCheck{NormalizedMentioned: true, AppID: "cli_a"}) {
		t.Fatal("normalized mentioned bot should be treated as bot mention")
	}
}

func TestIsMentionedBotRecognizesBotMentionWhenIDDiffersFromAppID(t *testing.T) {
	mentions := []*larkim.MentionEvent{newTypedMention("ou_bot_open_id", "app")}

	if !isMentionedBotFromParts(feishuMentionCheck{Mentions: mentions, Content: "@_user_1 hello", AppID: "cli_a"}) {
		t.Fatal("bot/app typed mention should be treated as bot mention even when id is not app_id")
	}
}

func TestIsMentionedBotDoesNotTreatOtherBotMentionAsCurrentBot(t *testing.T) {
	mentions := []*larkim.MentionEvent{newTypedMention("ou_other_bot", "app")}
	mentions[0].Key = stringPtr("")

	if isMentionedBotFromParts(feishuMentionCheck{Mentions: mentions, Content: "@_other_bot hello", AppID: "cli_a"}) {
		t.Fatal("other bot mention with empty key should not be treated as current bot mention")
	}
}

func TestIsMentionedBotDoesNotTreatUserMentionAsBot(t *testing.T) {
	mentions := []*larkim.MentionEvent{newTypedMention("ou_other_user", "user")}

	if isMentionedBotFromParts(feishuMentionCheck{Mentions: mentions, Content: "@_user_1 hello", AppID: "cli_a"}) {
		t.Fatal("ordinary user mention should not be treated as bot mention")
	}
}

func TestToIncomingFromMessageDispatchesBotTypedMentionedGroup(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := newMessageEvent("group", "text", `{"text":"@_user_1 hello"}`)
	event.Event.Message.Mentions = []*larkim.MentionEvent{newTypedMention("ou_bot_open_id", "app")}

	incoming, ok := adapter.toIncomingFromMessage(context.Background(), event)

	if !ok {
		t.Fatal("bot/app typed mention group message should be dispatchable")
	}
	if incoming.Metadata["feishu_is_mentioned"] != "true" {
		t.Fatalf("metadata=%#v, want feishu_is_mentioned=true", incoming.Metadata)
	}
}

func TestToIncomingFromMessageDispatchesGroupWhenMentionNotRequired(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.SetSessionOptions(FeishuSessionOptions{RequireMentionInGroup: false, ThreadIsolation: true})
	event := newMessageEvent("group", "text", `{"text":"hello"}`)
	event.Event.Message.ThreadId = stringPtr("omt_thread")

	incoming, ok := adapter.toIncomingFromMessage(context.Background(), event)

	if !ok {
		t.Fatal("group message should dispatch when require_mention_in_group=false")
	}
	if incoming.Metadata["feishu_session_key"] != "feishu:tenant_1:group:oc_1:omt_thread" {
		t.Fatalf("metadata=%#v, want scoped thread session", incoming.Metadata)
	}
}

func TestToIncomingFromMessageDownloadsImage(t *testing.T) {
	downloader := &fakeResourceDownloader{attachments: []platform.Attachment{{
		Kind:     platform.AttachmentImage,
		Path:     "/tmp/image.png",
		SourceID: "img_1",
	}}}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = downloader
	event := newMessageEvent("p2p", "image", `{"image_key":"img_1"}`)

	incoming, ok := adapter.toIncomingFromMessage(context.Background(), event)

	if !ok {
		t.Fatal("toIncomingFromMessage ok=false, want true")
	}
	if len(incoming.Attachments) != 1 || incoming.Attachments[0].Kind != platform.AttachmentImage {
		t.Fatalf("attachments=%#v, want one image", incoming.Attachments)
	}
	if len(downloader.seen) != 1 || downloader.seen[0].FileKey != "img_1" {
		t.Fatalf("downloaded resources=%#v, want img_1", downloader.seen)
	}
}

func TestToIncomingFromMessageParsesPost(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = &fakeResourceDownloader{}
	content := `{"zh_cn":{"title":"标题","content":[[{"tag":"text","text":"第一段"},{"tag":"a","text":"链接","href":"https://example.com"}],[{"tag":"text","text":"第二段"}]]}}`
	event := newMessageEvent("p2p", "post", content)

	incoming, ok := adapter.toIncomingFromMessage(context.Background(), event)

	if !ok {
		t.Fatal("toIncomingFromMessage ok=false, want true")
	}
	if incoming.Text == "" || incoming.Text == "[rich text message]" {
		t.Fatalf("post text=%q, want extracted rich text", incoming.Text)
	}
}

func TestToIncomingFromMessageParsesPostWithImage(t *testing.T) {
	downloader := &fakeResourceDownloader{attachments: []platform.Attachment{{
		Kind:     platform.AttachmentImage,
		Path:     "/tmp/img_1.png",
		SourceID: "img_1",
	}}}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = downloader
	content := `{"zh_cn":{"content":[[{"tag":"text","text":"请看这张图"},{"tag":"img","image_key":"img_1"}]]}}`
	event := newMessageEvent("p2p", "post", content)

	incoming, ok := adapter.toIncomingFromMessage(context.Background(), event)

	if !ok {
		t.Fatal("toIncomingFromMessage ok=false, want true")
	}
	if incoming.Text != "请看这张图" {
		t.Fatalf("post text=%q, want extracted text without image marker", incoming.Text)
	}
	if strings.Contains(incoming.Text, "img_1") || strings.Contains(incoming.Text, "![image]") {
		t.Fatalf("post text=%q, want no raw image key marker", incoming.Text)
	}
	if len(incoming.Attachments) != 1 || incoming.Attachments[0].Kind != platform.AttachmentImage {
		t.Fatalf("attachments=%#v, want one image", incoming.Attachments)
	}
	if len(downloader.seen) != 1 || downloader.seen[0].Type != "image" || downloader.seen[0].FileKey != "img_1" {
		t.Fatalf("downloaded resources=%#v, want image img_1", downloader.seen)
	}
}

func TestToIncomingFromMessageParsesPostContentObjectFallback(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.downloader = &fakeResourceDownloader{}
	content := `{"zh_cn":{"content":{"blocks":[{"tag":"text","text":"非标准富文本"}]}}}`
	event := newMessageEvent("p2p", "post", content)

	incoming, ok := adapter.toIncomingFromMessage(context.Background(), event)

	if !ok {
		t.Fatal("toIncomingFromMessage ok=false, want true")
	}
	if incoming.Text != "非标准富文本" {
		t.Fatalf("post text=%q, want fallback extracted text", incoming.Text)
	}
}

// newMessageEvent 构造飞书 P2 消息事件。
func newMessageEvent(chatType string, messageType string, content string) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{
			Header: &larkevent.EventHeader{TenantKey: "tenant_1"},
		},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{OpenId: stringPtr("ou_user")},
			},
			Message: &larkim.EventMessage{
				MessageId:   stringPtr("om_1"),
				ChatId:      stringPtr("oc_1"),
				ChatType:    stringPtr(chatType),
				MessageType: stringPtr(messageType),
				Content:     stringPtr(content),
			},
		},
	}
}

// newMention 构造飞书 @ 事件条目。
func newMention(openID string) *larkim.MentionEvent {
	return &larkim.MentionEvent{
		Key: stringPtr("@_user_1"),
		Id:  &larkim.UserId{OpenId: stringPtr(openID)},
	}
}

// newTypedMention 构造带身份类型的飞书 @ 事件条目。
func newTypedMention(openID string, mentionedType string) *larkim.MentionEvent {
	mention := newMention(openID)
	mention.MentionedType = stringPtr(mentionedType)
	return mention
}

// stringPtr 返回字符串指针，匹配飞书 SDK 事件模型。
func stringPtr(value string) *string {
	return &value
}
