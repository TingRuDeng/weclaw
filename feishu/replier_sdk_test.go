package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestSDKMessageSenderSendsPermissionGuideCardOnPermissionError(t *testing.T) {
	sender := newTestSDKMessageSender("cli_a", []createMessageResult{
		{code: 99991663, msg: "missing scope"},
		{},
	})

	err := sender.SendText(context.Background(), "ou_user", "hello")

	if !IsPermissionError(err) {
		t.Fatalf("SendText error=%v, want permission error", err)
	}
	if len(sender.calls) != 2 {
		t.Fatalf("calls=%#v, want original send and guide card", sender.calls)
	}
	if sender.calls[1].msgType != "interactive" || !strings.Contains(sender.calls[1].content, "im:message") {
		t.Fatalf("guide call=%#v, want permission card", sender.calls[1])
	}
}

func TestSDKMessageSenderFallsBackToTextPermissionGuide(t *testing.T) {
	sender := newTestSDKMessageSender("cli_a", []createMessageResult{
		{code: 99991663, msg: "missing scope"},
		{err: errors.New("card rejected")},
		{},
	})

	err := sender.SendText(context.Background(), "ou_user", "hello")

	if !IsPermissionError(err) {
		t.Fatalf("SendText error=%v, want original permission error", err)
	}
	if len(sender.calls) != 3 {
		t.Fatalf("calls=%#v, want original send, guide card, guide text", sender.calls)
	}
	content := decodeTextContent(t, sender.calls[2].content)
	if sender.calls[2].msgType != "text" || !strings.Contains(content, "https://open.feishu.cn/app/cli_a/permission") {
		t.Fatalf("fallback call=%#v content=%q, want permission text", sender.calls[2], content)
	}
}

func TestSDKMessageSenderPermissionGuideLimiterSuppressesRepeatedGuide(t *testing.T) {
	sender := newTestSDKMessageSender("cli_a", []createMessageResult{
		{code: 99991663, msg: "missing scope"},
		{},
		{code: 99991663, msg: "missing scope"},
	})

	_ = sender.SendText(context.Background(), "ou_user", "first")
	_ = sender.SendText(context.Background(), "ou_user", "second")

	if len(sender.calls) != 3 {
		t.Fatalf("calls=%#v, want second permission error without repeated guide", sender.calls)
	}
}

func TestSDKMessageSenderUsesChatIDReceiveTypeForChatID(t *testing.T) {
	sender := newTestSDKMessageSender("cli_a", nil)

	err := sender.SendText(context.Background(), "oc_chat", "hello")

	if err != nil {
		t.Fatalf("SendText error: %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("calls=%#v, want one message", sender.calls)
	}
	if sender.calls[0].receiveIDType != larkim.CreateMessageV1ReceiveIDTypeChatId {
		t.Fatalf("receiveIDType=%q, want chat_id", sender.calls[0].receiveIDType)
	}
}

func TestSDKMessageSenderRepliesInThread(t *testing.T) {
	sender := newTestSDKMessageSender("cli_a", nil)

	err := sender.ReplyText(context.Background(), "om_root", "hello")

	if err != nil {
		t.Fatalf("ReplyText error: %v", err)
	}
	if len(sender.replyCalls) != 1 {
		t.Fatalf("replyCalls=%#v, want one reply", sender.replyCalls)
	}
	call := sender.replyCalls[0]
	if call.messageID != "om_root" || call.msgType != larkim.MsgTypeText {
		t.Fatalf("reply call=%#v, want text reply to om_root", call)
	}
	if !call.replyInThread {
		t.Fatalf("reply call=%#v, want reply_in_thread=true", call)
	}
	if decodeTextContent(t, call.content) != "hello" {
		t.Fatalf("reply content=%q, want hello", call.content)
	}
}

type createMessageCall struct {
	receiveID     string
	receiveIDType string
	msgType       string
	content       string
}

type replyMessageCall struct {
	messageID     string
	msgType       string
	content       string
	replyInThread bool
}

type createMessageResult struct {
	code int
	msg  string
	err  error
}

type testSDKMessageSender struct {
	*sdkMessageSender
	calls      []createMessageCall
	replyCalls []replyMessageCall
}

func newTestSDKMessageSender(appID string, results []createMessageResult) *testSDKMessageSender {
	sender := &testSDKMessageSender{
		sdkMessageSender: &sdkMessageSender{appID: appID, guide: newPermissionGuideLimiter(appID)},
	}
	sender.create = func(ctx context.Context, receiveID string, receiveIDType string, msgType string, content string) (int, string, error) {
		sender.calls = append(sender.calls, createMessageCall{receiveID: receiveID, receiveIDType: receiveIDType, msgType: msgType, content: content})
		if len(results) == 0 {
			return 0, "", nil
		}
		result := results[0]
		results = results[1:]
		return result.code, result.msg, result.err
	}
	sender.reply = func(ctx context.Context, messageID string, msgType string, content string, replyInThread bool) (int, string, error) {
		sender.replyCalls = append(sender.replyCalls, replyMessageCall{messageID: messageID, msgType: msgType, content: content, replyInThread: replyInThread})
		return 0, "", nil
	}
	return sender
}

func decodeTextContent(t *testing.T, content string) string {
	t.Helper()
	var data map[string]string
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		t.Fatalf("decode text content: %v", err)
	}
	return data["text"]
}
