package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type fakeMessageSender struct {
	texts  []string
	images []string
	files  []string
	cards  []string
}

// SendText 记录测试发送的文本。
func (f *fakeMessageSender) SendText(ctx context.Context, openID string, text string) error {
	f.texts = append(f.texts, openID+":"+text)
	return nil
}

// SendImage 记录测试发送的图片路径。
func (f *fakeMessageSender) SendImage(ctx context.Context, openID string, localPath string) error {
	f.images = append(f.images, openID+":"+localPath)
	return nil
}

func (f *fakeMessageSender) SendFile(ctx context.Context, openID string, localPath string) error {
	f.files = append(f.files, openID+":"+localPath)
	return nil
}

// SendCard 记录测试发送的卡片 ID。
func (f *fakeMessageSender) SendCard(ctx context.Context, openID string, cardID string) error {
	f.cards = append(f.cards, openID+":"+cardID)
	return nil
}

func TestReplierSendTextSplitsLongText(t *testing.T) {
	sender := &fakeMessageSender{}
	reply := NewReplier(sender, "ou_user")
	text := strings.Repeat("你", feishuTextChunkRunes+1)

	if err := reply.SendText(context.Background(), text); err != nil {
		t.Fatalf("SendText error: %v", err)
	}
	if len(sender.texts) != 2 {
		t.Fatalf("texts=%d, want 2 chunks", len(sender.texts))
	}
}

func TestReplierSendImageUsesSender(t *testing.T) {
	sender := &fakeMessageSender{}
	reply := NewReplier(sender, "ou_user")

	if err := reply.SendImage(context.Background(), "/tmp/a.png"); err != nil {
		t.Fatalf("SendImage error: %v", err)
	}
	if len(sender.images) != 1 || sender.images[0] != "ou_user:/tmp/a.png" {
		t.Fatalf("images=%#v, want image path", sender.images)
	}
}

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

func TestReplierAskChoicesSendsCardWhenCardKitAvailable(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardID: "card-choice"}
	reply := NewReplier(sender, "ou_user", cardKit)

	err := reply.AskChoices(context.Background(), "请选择", []platform.Choice{{ID: "1", Label: "继续"}})

	if err != nil {
		t.Fatalf("AskChoices error: %v", err)
	}
	if len(cardKit.createdCards) != 1 {
		t.Fatalf("createdCards=%d, want 1", len(cardKit.createdCards))
	}
	if len(sender.cards) != 1 || sender.cards[0] != "ou_user:card-choice" {
		t.Fatalf("cards=%#v, want sent choice card", sender.cards)
	}
	if len(sender.texts) != 0 {
		t.Fatalf("texts=%#v, want no text fallback", sender.texts)
	}
}

func TestReplierAskChoicesIncludesCurrentTaskCardID(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardIDs: []string{"card-task-1", "card-panel-1"}}
	reply := newReplierWithTaskCards(sender, "ou_user", cardKit, newTaskCardRegistry())

	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialContent: "thinking"})
	if err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}
	if stream == nil || reply.CurrentTaskCardID() != "card-task-1" {
		t.Fatalf("current task card=%q, want card-task-1", reply.CurrentTaskCardID())
	}
	err = reply.AskChoices(context.Background(), "Codex 请求执行敏感操作，请确认：\n\n{\"cmd\":\"date\"}", []platform.Choice{{ID: "accept", Label: "accept"}})
	if err != nil {
		t.Fatalf("AskChoices error: %v", err)
	}
	if len(cardKit.createdCards) != 2 {
		t.Fatalf("createdCards=%d, want task card and approval card", len(cardKit.createdCards))
	}
	card := decodeCardJSON(t, cardKit.createdCards[1])
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	value := elements[2].(map[string]any)["value"].(map[string]any)
	if value["task_card_id"] != "card-task-1" {
		t.Fatalf("button value=%#v, want task card id", value)
	}
	if value["approval_panel"] != "1" {
		t.Fatalf("button value=%#v, want approval panel marker", value)
	}
}

func TestReplierAskChoicesAggregatesApprovalsIntoOnePanel(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardIDs: []string{"card-task-1", "card-panel-1"}}
	reply := newReplierWithTaskCards(sender, "ou_user", cardKit, newTaskCardRegistry())
	if _, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialContent: "thinking"}); err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}

	err := reply.AskChoices(context.Background(), "Codex 请求执行敏感操作，请确认：\n\n{\"cmd\":\"date\"}", []platform.Choice{{
		ID:       "accept",
		Label:    "允许本次",
		Metadata: map[string]string{"approval_key": "approval-1"},
	}})
	if err != nil {
		t.Fatalf("first AskChoices error: %v", err)
	}
	err = reply.AskChoices(context.Background(), "Codex 请求执行敏感操作，请确认：\n\n{\"cmd\":\"pwd\"}", []platform.Choice{{
		ID:       "accept",
		Label:    "允许本次",
		Metadata: map[string]string{"approval_key": "approval-2"},
	}})
	if err != nil {
		t.Fatalf("second AskChoices error: %v", err)
	}

	if len(sender.cards) != 2 || sender.cards[1] != "ou_user:card-panel-1" {
		t.Fatalf("sent cards=%#v, want task card and one approval panel", sender.cards)
	}
	if cardKit.updateCountFor("card-panel-1") != 1 {
		t.Fatalf("updated card ids=%#v, want update existing approval panel", cardKit.updateCardIDs)
	}
	panel := decodeCardJSON(t, cardKit.updateCards[0])
	body := panel["body"].(map[string]any)
	content := body["elements"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(content, "待处理审批：2 个") {
		t.Fatalf("panel content=%q, want two pending approvals", content)
	}
}

func TestReplierAskChoicesRollsBackPanelItemWhenUpdateFails(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{
		cardIDs:      []string{"card-task-1", "card-panel-1", "card-fallback-1"},
		updateErrors: []error{context.Canceled},
	}
	reply := newReplierWithTaskCards(sender, "ou_user", cardKit, newTaskCardRegistry())
	if _, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialContent: "thinking"}); err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}
	if err := reply.AskChoices(context.Background(), approvalPromptForTest("date"), approvalChoiceForTest("approval-1")); err != nil {
		t.Fatalf("first AskChoices error: %v", err)
	}
	if err := reply.AskChoices(context.Background(), approvalPromptForTest("pwd"), approvalChoiceForTest("approval-2")); err != nil {
		t.Fatalf("second AskChoices error: %v", err)
	}
	if err := reply.AskChoices(context.Background(), approvalPromptForTest("whoami"), approvalChoiceForTest("approval-3")); err != nil {
		t.Fatalf("third AskChoices error: %v", err)
	}

	if len(sender.cards) != 3 || sender.cards[2] != "ou_user:card-fallback-1" {
		t.Fatalf("sent cards=%#v, want fallback independent approval card", sender.cards)
	}
	if cardKit.updateCountFor("card-panel-1") != 2 {
		t.Fatalf("updated card ids=%#v, want failed update plus later success", cardKit.updateCardIDs)
	}
	panel := decodeCardJSON(t, cardKit.updateCards[1])
	body := panel["body"].(map[string]any)
	content := approvalPanelContentForTest(body)
	if !strings.Contains(content, "待处理审批：2 个") || strings.Contains(content, "pwd") {
		t.Fatalf("panel content=%q, want rollback of failed second approval", content)
	}
}

func approvalPromptForTest(command string) string {
	return "Codex 请求执行敏感操作，请确认：\n\n{\"cmd\":\"" + command + "\"}"
}

func approvalChoiceForTest(key string) []platform.Choice {
	return []platform.Choice{{
		ID:       "accept",
		Label:    "允许本次",
		Metadata: map[string]string{"approval_key": key},
	}}
}

func approvalPanelContentForTest(body map[string]any) string {
	elements := body["elements"].([]any)
	parts := make([]string, 0, len(elements))
	for _, element := range elements {
		if content, ok := element.(map[string]any)["content"].(string); ok {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n")
}

func TestReplierTypingUsesThinkingCard(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardID: "card-typing"}
	reply := NewReplier(sender, "ou_user", cardKit)

	if err := reply.Typing(context.Background(), true); err != nil {
		t.Fatalf("Typing true error: %v", err)
	}
	if err := reply.Typing(context.Background(), true); err != nil {
		t.Fatalf("Typing true again error: %v", err)
	}
	if err := reply.Typing(context.Background(), false); err != nil {
		t.Fatalf("Typing false error: %v", err)
	}

	if len(cardKit.createdCards) != 1 {
		t.Fatalf("createdCards=%d, want one thinking card", len(cardKit.createdCards))
	}
	if len(sender.cards) != 1 || sender.cards[0] != "ou_user:card-typing" {
		t.Fatalf("cards=%#v, want one sent typing card", sender.cards)
	}
	if len(cardKit.updateSeqs) != 1 {
		t.Fatalf("updateSeqs=%#v, want done update", cardKit.updateSeqs)
	}
}

func TestTextFinalStreamSendsOnlyFinalContent(t *testing.T) {
	sender := &fakeMessageSender{}
	reply := NewReplier(sender, "ou_user")
	stream, err := reply.OpenStream(context.Background(), platformStreamOptions())
	if err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}

	if err := stream.Update(context.Background(), "partial"); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if err := stream.Complete(context.Background(), "done"); err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if len(sender.texts) != 1 || sender.texts[0] != "ou_user:done" {
		t.Fatalf("texts=%#v, want only final content", sender.texts)
	}
}

// platformStreamOptions 返回空流选项，避免测试直接依赖业务含义。
func platformStreamOptions() platform.StreamOptions {
	return platform.StreamOptions{}
}

type createMessageCall struct {
	receiveID     string
	receiveIDType string
	msgType       string
	content       string
}

type createMessageResult struct {
	code int
	msg  string
	err  error
}

type testSDKMessageSender struct {
	*sdkMessageSender
	calls []createMessageCall
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
