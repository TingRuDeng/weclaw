package feishu

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

type fakeMessageSender struct {
	texts  []string
	images []string
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
