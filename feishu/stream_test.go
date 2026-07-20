package feishu

import (
	"context"
	"errors"
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
	updateCardCh  chan string
	streamStarted chan struct{}
	streamBlock   <-chan struct{}
}

type fakeIdempotentCardKitClient struct {
	fakeCardKitClient
	streamOperations []string
	updateOperations []string
	seen             map[string]bool
}

func (f *fakeIdempotentCardKitClient) SetStreamingIdempotent(ctx context.Context, cardID string, enabled bool, sequence int, operationID string) error {
	f.streamOperations = append(f.streamOperations, operationID)
	if f.seen == nil {
		f.seen = make(map[string]bool)
	}
	if f.seen[operationID] {
		return nil
	}
	f.seen[operationID] = true
	return f.SetStreaming(ctx, cardID, enabled, sequence)
}

func (f *fakeIdempotentCardKitClient) UpdateCardIdempotent(ctx context.Context, cardID string, cardJSON string, sequence int, operationID string) error {
	f.updateOperations = append(f.updateOperations, operationID)
	if f.seen == nil {
		f.seen = make(map[string]bool)
	}
	if f.seen[operationID] {
		return nil
	}
	f.seen[operationID] = true
	return f.UpdateCard(ctx, cardID, cardJSON, sequence)
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

func (f *fakeCardKitClient) SetStreamingIdempotent(ctx context.Context, cardID string, enabled bool, sequence int, _ string) error {
	return f.SetStreaming(ctx, cardID, enabled, sequence)
}

// StreamContent 记录增量更新顺序号并按需返回错误。
func (f *fakeCardKitClient) StreamContent(ctx context.Context, cardID string, elementID string, content string, sequence int) error {
	f.streamSeqs = append(f.streamSeqs, sequence)
	f.streamTexts = append(f.streamTexts, content)
	if f.streamStarted != nil {
		select {
		case f.streamStarted <- struct{}{}:
		default:
		}
	}
	if f.streamBlock != nil {
		select {
		case <-f.streamBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
	if f.updateCardCh != nil {
		f.updateCardCh <- cardJSON
	}
	if len(f.updateErrors) == 0 {
		return nil
	}
	err := f.updateErrors[0]
	f.updateErrors = f.updateErrors[1:]
	return err
}

func (f *fakeCardKitClient) UpdateCardIdempotent(ctx context.Context, cardID string, cardJSON string, sequence int, _ string) error {
	return f.UpdateCard(ctx, cardID, cardJSON, sequence)
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

func TestFeishuStreamUpdateDoesNotHoldStateLockDuringNetwork(t *testing.T) {
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	cardKit := &fakeCardKitClient{streamStarted: started, streamBlock: block}
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	stream := &feishuStream{cardKit: cardKit, cardID: "card-1", sequence: 1, throttle: time.Hour, now: func() time.Time { return now }}

	firstDone := make(chan error, 1)
	go func() { firstDone <- stream.Update(context.Background(), "one") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first update did not enter cardKit call")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- stream.Update(context.Background(), "two") }()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second Update error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second Update blocked on stream state lock while first network call was pending")
	}
	close(block)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first Update error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first Update did not finish after network unblock")
	}
	if err := stream.Complete(context.Background(), "done"); err != nil {
		t.Fatalf("Complete error: %v", err)
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

func TestTaskCardStreamUpdateReplacesProgressContent(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "打包发布", Content: "正在分析任务，请稍候。"})
	now := time.Date(2026, 7, 8, 15, 28, 0, 0, time.UTC)
	stream := &feishuStream{cardKit: cardKit, taskCards: registry, cardID: "card-1", title: "打包发布", sequence: 1, throttle: cardkitThrottle, now: func() time.Time { return now }}

	first := "进展：Codex 正在执行命令并产生输出。"
	second := "进展：Codex 正在运行测试。"
	if err := stream.Update(context.Background(), first); err != nil {
		t.Fatalf("Update first error: %v", err)
	}
	now = now.Add(cardkitThrottle)
	if err := stream.Update(context.Background(), second); err != nil {
		t.Fatalf("Update second error: %v", err)
	}

	if len(cardKit.streamTexts) != 0 {
		t.Fatalf("task card progress should not use append-style stream content, got %#v", cardKit.streamTexts)
	}
	if len(cardKit.updateCards) != 2 {
		t.Fatalf("update cards=%d, want replacing updates", len(cardKit.updateCards))
	}
	card := decodeCardJSON(t, cardKit.updateCards[1])
	body := card["body"].(map[string]any)
	main := body["elements"].([]any)[1].(map[string]any)
	if main["content"] != second {
		t.Fatalf("main content=%q, want latest progress only", main["content"])
	}
	if strings.Contains(main["content"].(string), first) {
		t.Fatalf("main content=%q should not keep previous progress", main["content"])
	}
}

func TestTaskCardStreamCoalescesThrottledUpdatesAndFlushesLatest(t *testing.T) {
	cardKit := &fakeCardKitClient{updateCardCh: make(chan string, 3)}
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "Codex", Content: "处理中"})
	stream := &feishuStream{
		cardKit: cardKit, taskCards: registry, cardID: "card-1", title: "Codex",
		sequence: 1, throttle: 20 * time.Millisecond, now: time.Now,
	}

	if err := stream.Update(context.Background(), "进展一"); err != nil {
		t.Fatalf("Update first error: %v", err)
	}
	select {
	case <-cardKit.updateCardCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("first progress update was not written")
	}
	if err := stream.Update(context.Background(), "进展二"); err != nil {
		t.Fatalf("Update second error: %v", err)
	}
	if err := stream.Update(context.Background(), "进展三"); err != nil {
		t.Fatalf("Update third error: %v", err)
	}

	var latestCard string
	select {
	case latestCard = <-cardKit.updateCardCh:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("latest throttled progress was never flushed")
	}
	card := decodeCardJSON(t, latestCard)
	body := card["body"].(map[string]any)
	main := body["elements"].([]any)[1].(map[string]any)
	if got := main["content"]; got != "进展三" {
		t.Fatalf("coalesced progress=%q, want latest progress", got)
	}
}

func TestTaskCardStreamCompleteCancelsPendingProgress(t *testing.T) {
	cardKit := &fakeCardKitClient{updateCardCh: make(chan string, 3)}
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "Codex", Content: "处理中"})
	stream := &feishuStream{
		cardKit: cardKit, taskCards: registry, cardID: "card-1", title: "Codex",
		sequence: 1, throttle: 20 * time.Millisecond, now: time.Now,
	}

	if err := stream.Update(context.Background(), "进展一"); err != nil {
		t.Fatalf("Update first error: %v", err)
	}
	<-cardKit.updateCardCh
	if err := stream.Update(context.Background(), "待补发进展"); err != nil {
		t.Fatalf("Update pending error: %v", err)
	}
	if err := stream.Complete(context.Background(), "最终结果"); err != nil {
		t.Fatalf("Complete error: %v", err)
	}

	finalCard := <-cardKit.updateCardCh
	card := decodeCardJSON(t, finalCard)
	body := card["body"].(map[string]any)
	main := body["elements"].([]any)[1].(map[string]any)
	if got := main["content"]; got != "最终结果" {
		t.Fatalf("final content=%q, want 最终结果", got)
	}
	select {
	case lateCard := <-cardKit.updateCardCh:
		t.Fatalf("pending progress overwrote completed card: %s", lateCard)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTaskCardStreamSupersedeStopsOldCardWithoutCompletingTask(t *testing.T) {
	cardKit := &fakeCardKitClient{updateCardCh: make(chan string, 4)}
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "Codex · project-a", Content: "处理中"})
	stream := &feishuStream{
		cardKit: cardKit, taskCards: registry, cardID: "card-1", title: "Codex · project-a",
		sequence: 1, throttle: 20 * time.Millisecond, now: time.Now,
	}

	if err := stream.Update(context.Background(), "进展 A"); err != nil {
		t.Fatalf("Update first error: %v", err)
	}
	<-cardKit.updateCardCh
	if err := stream.Update(context.Background(), "待补发进展"); err != nil {
		t.Fatalf("Update pending error: %v", err)
	}
	if err := stream.Supersede(context.Background(), "已在新位置继续展示"); err != nil {
		t.Fatalf("Supersede error: %v", err)
	}

	cardJSON := <-cardKit.updateCardCh
	card := decodeCardJSON(t, cardJSON)
	config := card["config"].(map[string]any)
	if streaming, _ := config["streaming_mode"].(bool); streaming {
		t.Fatalf("superseded card must stop streaming: %#v", config)
	}
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if got := elements[0].(map[string]any)["content"]; got != "**已转移**" {
		t.Fatalf("status=%q, want 已转移", got)
	}
	if got := elements[1].(map[string]any)["content"]; got != "已在新位置继续展示" {
		t.Fatalf("content=%q", got)
	}
	if stream.terminal != nil || len(cardKit.destroyed) != 0 {
		t.Fatalf("supersede must not create terminal checkpoint or destroy card: terminal=%#v destroyed=%#v", stream.terminal, cardKit.destroyed)
	}

	if err := stream.Update(context.Background(), "迟到进展"); err != nil {
		t.Fatalf("late Update error: %v", err)
	}
	if err := stream.Complete(context.Background(), "错误终态"); err != nil {
		t.Fatalf("late Complete error: %v", err)
	}
	select {
	case late := <-cardKit.updateCardCh:
		t.Fatalf("superseded card received late update: %s", late)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTaskCardStreamDoesNotFlushStaleProgressAfterRevert(t *testing.T) {
	cardKit := &fakeCardKitClient{updateCardCh: make(chan string, 3)}
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "Codex", Content: "处理中"})
	stream := &feishuStream{
		cardKit: cardKit, taskCards: registry, cardID: "card-1", title: "Codex",
		sequence: 1, throttle: 20 * time.Millisecond, now: time.Now,
	}

	if err := stream.Update(context.Background(), "进展 A"); err != nil {
		t.Fatalf("Update A error: %v", err)
	}
	<-cardKit.updateCardCh
	if err := stream.Update(context.Background(), "进展 B"); err != nil {
		t.Fatalf("Update B error: %v", err)
	}
	if err := stream.Update(context.Background(), "进展 A"); err != nil {
		t.Fatalf("Update reverted A error: %v", err)
	}

	select {
	case staleCard := <-cardKit.updateCardCh:
		t.Fatalf("stale pending progress was flushed after reverting to displayed content: %s", staleCard)
	case <-time.After(50 * time.Millisecond):
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

func TestFeishuStreamCompleteWithEmptyContentClearsTaskCardProgress(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "Codex", Content: "处理中"})
	registry.updateContent("card-1", "进展：任务仍在执行中，连接正常。")
	stream := &feishuStream{cardKit: cardKit, taskCards: registry, cardID: "card-1", title: "Codex", sequence: 4, throttle: cardkitThrottle, now: time.Now}

	if err := stream.Complete(context.Background(), ""); err != nil {
		t.Fatalf("Complete error: %v", err)
	}

	card := decodeCardJSON(t, cardKit.updateCards[0])
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if len(elements) != 1 {
		t.Fatalf("elements=%d, want status-only done card: %#v", len(elements), elements)
	}
	status := elements[0].(map[string]any)
	if status["element_id"] != "status" || status["content"] != "**已完成**" {
		t.Fatalf("status element=%#v, want done status only", status)
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

func TestFeishuTerminalCheckpointKeepsOperationIDsAcrossRestartRetry(t *testing.T) {
	cardKit := &fakeIdempotentCardKitClient{}
	stream := &feishuStream{
		cardKit: cardKit, cardID: "card-1", title: "Codex", sequence: 4,
		throttle: cardkitThrottle, now: time.Now,
	}
	checkpoint, err := stream.PrepareTerminal("最终结果", false)
	if err != nil {
		t.Fatalf("PrepareTerminal: %v", err)
	}
	restarted := NewReplier(nil, "oc_chat", cardKit)
	for attempt := 0; attempt < 2; attempt++ {
		if err := restarted.DeliverTerminal(context.Background(), checkpoint); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if len(cardKit.streamOperations) != 2 || cardKit.streamOperations[0] == "" || cardKit.streamOperations[0] != cardKit.streamOperations[1] {
		t.Fatalf("stream operations=%#v", cardKit.streamOperations)
	}
	if len(cardKit.updateOperations) != 2 || cardKit.updateOperations[0] == "" || cardKit.updateOperations[0] != cardKit.updateOperations[1] {
		t.Fatalf("update operations=%#v", cardKit.updateOperations)
	}
	if len(cardKit.streamingSeqs) != 1 || len(cardKit.updateSeqs) != 1 || cardKit.updateSeqs[0] <= cardKit.streamingSeqs[0] {
		t.Fatalf("stream seqs=%#v update seqs=%#v", cardKit.streamingSeqs, cardKit.updateSeqs)
	}
}

func TestFeishuTerminalCheckpointRejectsNonIdempotentClient(t *testing.T) {
	base := &fakeCardKitClient{}
	stream := &feishuStream{
		cardKit: base, cardID: "card-1", title: "Codex", sequence: 4,
		throttle: cardkitThrottle, now: time.Now,
	}
	checkpoint, err := stream.PrepareTerminal("最终结果", false)
	if err != nil {
		t.Fatalf("PrepareTerminal: %v", err)
	}
	nonIdempotent := struct{ cardKitClient }{cardKitClient: base}
	restarted := NewReplier(nil, "oc_chat", nonIdempotent)

	err = restarted.DeliverTerminal(context.Background(), checkpoint)

	if !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("err=%v, want ErrUnsupported", err)
	}
	if len(base.streamingSeqs) != 0 || len(base.updateSeqs) != 0 {
		t.Fatalf("non-idempotent CardKit client must not be called: streaming=%#v updates=%#v", base.streamingSeqs, base.updateSeqs)
	}
}
