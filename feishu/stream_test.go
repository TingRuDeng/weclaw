package feishu

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

type fakeCardKitClient struct {
	cardID        string
	cardIDs       []string
	createdCards  []string
	streamingSeqs []int
	streamSeqs    []int
	streamTexts   []string
	updateSeqs    []int
	updateCards   []string
	updateCardIDs []string
	destroyed     []string
	streamErrors  []error
	updateErrors  []error
}

// CreateCard 记录创建卡片 JSON 并返回固定 card_id。
func (f *fakeCardKitClient) CreateCard(ctx context.Context, cardJSON string) (string, error) {
	f.createdCards = append(f.createdCards, cardJSON)
	if len(f.cardIDs) > 0 {
		cardID := f.cardIDs[0]
		f.cardIDs = f.cardIDs[1:]
		return cardID, nil
	}
	if f.cardID == "" {
		return "card-1", nil
	}
	return f.cardID, nil
}

// SetStreaming 记录 streaming_mode 更新顺序号。
func (f *fakeCardKitClient) SetStreaming(ctx context.Context, cardID string, enabled bool, sequence int) error {
	f.streamingSeqs = append(f.streamingSeqs, sequence)
	return nil
}

// StreamContent 记录增量更新顺序号并按需返回错误。
func (f *fakeCardKitClient) StreamContent(ctx context.Context, cardID string, elementID string, content string, sequence int) error {
	f.streamSeqs = append(f.streamSeqs, sequence)
	f.streamTexts = append(f.streamTexts, content)
	if len(f.streamErrors) == 0 {
		return nil
	}
	err := f.streamErrors[0]
	f.streamErrors = f.streamErrors[1:]
	return err
}

// UpdateCard 记录全量更新顺序号。
func (f *fakeCardKitClient) UpdateCard(ctx context.Context, cardID string, cardJSON string, sequence int) error {
	f.updateCardIDs = append(f.updateCardIDs, cardID)
	f.updateSeqs = append(f.updateSeqs, sequence)
	f.updateCards = append(f.updateCards, cardJSON)
	if len(f.updateErrors) == 0 {
		return nil
	}
	err := f.updateErrors[0]
	f.updateErrors = f.updateErrors[1:]
	return err
}

func (f *fakeCardKitClient) updateCountFor(cardID string) int {
	count := 0
	for _, got := range f.updateCardIDs {
		if got == cardID {
			count++
		}
	}
	return count
}

// DestroyCard 记录生命周期销毁调用。
func (f *fakeCardKitClient) DestroyCard(ctx context.Context, cardID string) error {
	f.destroyed = append(f.destroyed, cardID)
	return nil
}

func TestOpenStreamCreatesCardAndEnablesStreaming(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardID: "card-1"}
	reply := NewReplier(sender, "ou_user", cardKit)

	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialContent: "thinking"})

	if err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}
	if stream == nil || len(cardKit.createdCards) != 1 {
		t.Fatalf("stream=%#v created=%d, want created stream", stream, len(cardKit.createdCards))
	}
	if len(sender.cards) != 1 || sender.cards[0] != "ou_user:card-1" {
		t.Fatalf("cards=%#v, want sent card", sender.cards)
	}
	if len(cardKit.streamingSeqs) != 1 || cardKit.streamingSeqs[0] != 1 {
		t.Fatalf("streaming seqs=%#v, want enable seq 1", cardKit.streamingSeqs)
	}
}

func TestFeishuStreamUpdateThrottlesAndIncrementsSequence(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	stream := &feishuStream{cardKit: cardKit, cardID: "card-1", sequence: 1, throttle: cardkitThrottle, now: func() time.Time { return now }}

	if err := stream.Update(context.Background(), "one"); err != nil {
		t.Fatalf("Update one error: %v", err)
	}
	if err := stream.Update(context.Background(), "two"); err != nil {
		t.Fatalf("Update two error: %v", err)
	}
	now = now.Add(cardkitThrottle)
	if err := stream.Update(context.Background(), "three"); err != nil {
		t.Fatalf("Update three error: %v", err)
	}

	if len(cardKit.streamSeqs) != 2 || cardKit.streamSeqs[0] != 2 || cardKit.streamSeqs[1] != 3 {
		t.Fatalf("stream seqs=%#v, want [2 3]", cardKit.streamSeqs)
	}
}

func TestFeishuStreamReenablesStreamingOnInvalidState(t *testing.T) {
	cardKit := &fakeCardKitClient{
		streamErrors: []error{formatFeishuAPIError("cli_a", 200850, "invalid streaming")},
	}
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	stream := &feishuStream{cardKit: cardKit, cardID: "card-1", sequence: 1, throttle: cardkitThrottle, now: func() time.Time { return now }}

	if err := stream.Update(context.Background(), "one"); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if len(cardKit.streamingSeqs) != 1 || cardKit.streamingSeqs[0] != 3 {
		t.Fatalf("streaming seqs=%#v, want re-enable seq 3", cardKit.streamingSeqs)
	}
	if len(cardKit.streamSeqs) != 2 || cardKit.streamSeqs[1] != 4 {
		t.Fatalf("stream seqs=%#v, want retry seq 4", cardKit.streamSeqs)
	}
}

func TestFeishuStreamCompleteUpdatesDoneAndDestroys(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	stream := &feishuStream{cardKit: cardKit, cardID: "card-1", sequence: 4, throttle: cardkitThrottle, now: time.Now}

	if err := stream.Complete(context.Background(), "done"); err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if len(cardKit.streamingSeqs) != 1 || cardKit.streamingSeqs[0] != 5 {
		t.Fatalf("streaming seqs=%#v, want disable seq 5", cardKit.streamingSeqs)
	}
	if len(cardKit.updateSeqs) != 1 || cardKit.updateSeqs[0] != 6 {
		t.Fatalf("update seqs=%#v, want update seq 6", cardKit.updateSeqs)
	}
	if len(cardKit.destroyed) != 1 || cardKit.destroyed[0] != "card-1" {
		t.Fatalf("destroyed=%#v, want card-1", cardKit.destroyed)
	}
	card := decodeCardJSON(t, cardKit.updateCards[0])
	body := card["body"].(map[string]any)
	main := body["elements"].([]any)[1].(map[string]any)
	if main["content"] != "done" {
		t.Fatalf("final content=%#v, want done", main["content"])
	}
}

func TestFeishuStreamCompleteKeepsApprovalRecords(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "Codex", Content: "处理中"})
	registry.addApproval("card-1", parsedCardAction{Choice: "accept", Label: "accept", Summary: "command: date"})
	stream := &feishuStream{cardKit: cardKit, taskCards: registry, cardID: "card-1", sequence: 4, throttle: cardkitThrottle, now: time.Now}

	if err := stream.Complete(context.Background(), "最终结果"); err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	card := decodeCardJSON(t, cardKit.updateCards[0])
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if len(elements) != 3 {
		t.Fatalf("elements=%d, want approval record element", len(elements))
	}
	approval := elements[2].(map[string]any)
	if !strings.Contains(approval["content"].(string), "command: date") {
		t.Fatalf("approval content=%q, want approval record", approval["content"])
	}
}

func TestTaskCardApprovalUpdateKeepsStreamSequenceMonotonic(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "Codex", Content: "处理中"})
	stream := &feishuStream{cardKit: cardKit, taskCards: registry, cardID: "card-1", title: "Codex", sequence: 4, throttle: cardkitThrottle, now: time.Now}
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	adapter.taskCards = registry
	adapter.now = func() time.Time { return time.Date(2026, 7, 3, 10, 40, 0, 0, time.UTC) }

	if !adapter.updateTaskCardWithApproval(context.Background(), parsedCardAction{TaskCard: "card-1", Choice: "accept", Label: "允许本次", Summary: "command: date"}) {
		t.Fatal("approval update should update task card")
	}
	if err := stream.Complete(context.Background(), "最终结果"); err != nil {
		t.Fatalf("Complete error: %v", err)
	}

	if len(cardKit.updateSeqs) != 2 {
		t.Fatalf("update seqs=%#v, want approval update and final update", cardKit.updateSeqs)
	}
	if cardKit.updateSeqs[1] <= cardKit.updateSeqs[0] {
		t.Fatalf("update seqs=%#v, final update must be greater than approval update", cardKit.updateSeqs)
	}
}

func TestFeishuStreamCompleteIsIdempotentAndIgnoresLateUpdate(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	stream := &feishuStream{cardKit: cardKit, cardID: "card-1", sequence: 1, throttle: cardkitThrottle, now: func() time.Time { return now }}

	if err := stream.Update(context.Background(), "过程"); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if err := stream.Complete(context.Background(), "最终结果"); err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if err := stream.Update(context.Background(), "迟到片段"); err != nil {
		t.Fatalf("late Update error: %v", err)
	}
	if err := stream.Complete(context.Background(), "重复完成"); err != nil {
		t.Fatalf("second Complete error: %v", err)
	}

	if len(cardKit.streamTexts) != 1 || cardKit.streamTexts[0] != "过程" {
		t.Fatalf("stream texts=%#v, want only first update", cardKit.streamTexts)
	}
	if len(cardKit.updateCards) != 1 {
		t.Fatalf("update cards=%d, want one terminal update", len(cardKit.updateCards))
	}
}
