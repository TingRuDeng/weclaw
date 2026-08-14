package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

type fakeCardKitClient struct {
	cardID           string
	cardIDs          []string
	createdCards     []string
	streamingSeqs    []int
	streamSeqs       []int
	streamTexts      []string
	streamCardIDs    []string
	streamElementIDs []string
	updateSeqs       []int
	updateCards      []string
	updateCardIDs    []string
	destroyed        []string
	streamErrors     []error
	streamingErrors  []error
	updateErrors     []error
	updateCardCh     chan string
	streamStarted    chan struct{}
	streamBlock      <-chan struct{}
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
	if len(f.streamingErrors) > 0 {
		err := f.streamingErrors[0]
		f.streamingErrors = f.streamingErrors[1:]
		return err
	}
	return nil
}

func (f *fakeCardKitClient) SetStreamingIdempotent(ctx context.Context, cardID string, enabled bool, sequence int, _ string) error {
	return f.SetStreaming(ctx, cardID, enabled, sequence)
}

// StreamContent 记录增量更新顺序号并按需返回错误。
func (f *fakeCardKitClient) StreamContent(ctx context.Context, cardID string, elementID string, content string, sequence int) error {
	f.streamSeqs = append(f.streamSeqs, sequence)
	f.streamTexts = append(f.streamTexts, content)
	f.streamCardIDs = append(f.streamCardIDs, cardID)
	f.streamElementIDs = append(f.streamElementIDs, elementID)
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

func TestTaskCardStreamUpdatesOnlyCompleteProgressWithoutReplacingCard(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-task"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialContent: "初始", InitialPresentation: &platform.StreamPresentation{Summary: "开始", Details: "初始"}})
	if err != nil {
		t.Fatal(err)
	}
	structured, ok := stream.(platform.StructuredProgressStream)
	if !ok {
		t.Fatal("missing StructuredProgressStream")
	}
	stream.(*feishuStream).throttle = 0
	if err := structured.UpdatePresentation(context.Background(), platform.StreamPresentation{Summary: "正在运行测试", Details: "读取代码\n\n运行测试\n\n思考中....."}); err != nil {
		t.Fatal(err)
	}
	if err := structured.UpdatePresentation(context.Background(), platform.StreamPresentation{Summary: "测试完成", Details: "读取代码\n\n运行测试\n\n完成"}); err != nil {
		t.Fatal(err)
	}
	if len(kit.updateCardIDs) != 1 {
		t.Fatalf("UpdateCard calls=%d, want only the initial task-card action binding update", len(kit.updateCardIDs))
	}
	if len(kit.streamElementIDs) != 2 {
		t.Fatalf("element ids=%#v", kit.streamElementIDs)
	}
	want := []string{cardMainContentID, cardMainContentID}
	for i, got := range kit.streamElementIDs {
		if got != want[i] {
			t.Fatalf("element[%d]=%q want %q", i, got, want[i])
		}
	}
	for i := 1; i < len(kit.streamSeqs); i++ {
		if kit.streamSeqs[i] <= kit.streamSeqs[i-1] {
			t.Fatalf("sequences=%#v", kit.streamSeqs)
		}
	}
}

func TestTaskCardFirstStructuredPresentationUpgradesInitialCard(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-upgrade"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex", InitialContent: platform.TaskStreamThinkingIndicator,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream.(*feishuStream).throttle = 0

	err = stream.(platform.StructuredProgressStream).UpdatePresentation(context.Background(), platform.StreamPresentation{
		Summary: "正在读取代码", Details: "读取代码\n\n" + platform.TaskStreamThinkingIndicator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kit.updateCards) != 1 {
		t.Fatalf("UpdateCard calls=%d, want 1 layout upgrade", len(kit.updateCards))
	}
	if len(kit.streamElementIDs) != 0 {
		t.Fatalf("stream element ids=%#v, want no component update before layout upgrade", kit.streamElementIDs)
	}
	card := decodeCardJSON(t, kit.updateCards[0])
	elements := card["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 2 || elements[0].(map[string]any)["element_id"] != cardMainContentID ||
		elements[0].(map[string]any)["content"] != "读取代码\n\n"+platform.TaskStreamThinkingIndicator ||
		elements[1].(map[string]any)["element_id"] != cardProgressExpandID {
		t.Fatalf("upgraded body=%#v, want default preview and bottom expand control", card["body"])
	}

	err = stream.(platform.StructuredProgressStream).UpdatePresentation(context.Background(), platform.StreamPresentation{
		Summary: "正在运行测试", Details: "读取代码\n\n运行测试\n\n" + platform.TaskStreamThinkingIndicator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kit.updateCards) != 1 {
		t.Fatalf("UpdateCard calls=%d, want only the initial layout upgrade", len(kit.updateCards))
	}
	wantElementIDs := []string{cardMainContentID}
	if len(kit.streamElementIDs) != len(wantElementIDs) {
		t.Fatalf("stream element ids=%#v, want %#v after layout upgrade", kit.streamElementIDs, wantElementIDs)
	}
	for i, want := range wantElementIDs {
		if kit.streamElementIDs[i] != want {
			t.Fatalf("stream element id[%d]=%q, want %q", i, kit.streamElementIDs[i], want)
		}
	}
}

func TestTaskCardInitialStructuredPresentationAddsBottomCollapseControl(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-initial-progress"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	_, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex",
		InitialPresentation: &platform.StreamPresentation{
			Summary: "正在读取代码", Details: "读取代码\n\n" + platform.TaskStreamThinkingIndicator,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kit.updateCards) != 1 {
		t.Fatalf("UpdateCard calls=%d, want initial task-card action binding update", len(kit.updateCards))
	}
	card := decodeCardJSON(t, kit.updateCards[0])
	elements := card["body"].(map[string]any)["elements"].([]any)
	button := elements[len(elements)-1].(map[string]any)
	if button["element_id"] != cardProgressExpandID {
		t.Fatalf("elements=%#v, want bottom expand control", elements)
	}
	behaviors := button["behaviors"].([]any)
	value := behaviors[0].(map[string]any)["value"].(map[string]any)
	if value["task_card_id"] != "card-initial-progress" {
		t.Fatalf("button value=%#v, want created task card id", value)
	}
}

func TestCollapsibleTaskTerminalAndSupersedeShowPreviewBeforeExpandControl(t *testing.T) {
	for _, terminal := range []platform.StreamTerminalState{platform.StreamTerminalCompleted, platform.StreamTerminalFailed, platform.StreamTerminalStopped} {
		kit := &fakeCardKitClient{cardID: "card-terminal"}
		reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
		stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialContent: "初始", InitialPresentation: &platform.StreamPresentation{Summary: "摘要", Details: "详情"}})
		if err != nil {
			t.Fatal(err)
		}
		s := stream.(*feishuStream)
		op, err := s.prepareTerminalUpdate(cardStatusDone, "结果")
		if err != nil {
			t.Fatal(err)
		}
		card := decodeCardJSON(t, op.CardJSON)
		elems := card["body"].(map[string]any)["elements"].([]any)
		foundExpand, foundMain := false, false
		for _, e := range elems {
			switch e.(map[string]any)["element_id"] {
			case cardProgressExpandID:
				foundExpand = true
			case cardMainContentID:
				foundMain = true
			}
		}
		if !foundExpand || !foundMain {
			t.Fatalf("terminal elements=%#v, want preview and expand control", elems)
		}
		_ = terminal
	}
	kit := &fakeCardKitClient{cardID: "card-super"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialContent: "初始", InitialPresentation: &platform.StreamPresentation{Summary: "摘要", Details: "详情"}})
	if err != nil {
		t.Fatal(err)
	}
	op, err := stream.(*feishuStream).prepareSupersedeUpdate("转移")
	if err != nil {
		t.Fatal(err)
	}
	card := decodeCardJSON(t, op.CardJSON)
	elems := card["body"].(map[string]any)["elements"].([]any)
	foundExpand, foundMain := false, false
	for _, e := range elems {
		switch e.(map[string]any)["element_id"] {
		case cardProgressExpandID:
			foundExpand = true
		case cardMainContentID:
			foundMain = true
		}
	}
	if !foundExpand || !foundMain {
		t.Fatalf("supersede elements=%#v, want preview and expand control", elems)
	}
}

func TestTaskCardStructuredPresentationThrottleKeepsLatestSnapshot(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-throttle"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialPresentation: &platform.StreamPresentation{Summary: "初始", Details: "初始"}})
	if err != nil {
		t.Fatal(err)
	}
	s := stream.(*feishuStream)
	s.throttle = time.Hour
	structured := stream.(platform.StructuredProgressStream)
	if err := structured.UpdatePresentation(context.Background(), platform.StreamPresentation{Summary: "第一", Details: "第一详情"}); err != nil {
		t.Fatal(err)
	}
	if err := structured.UpdatePresentation(context.Background(), platform.StreamPresentation{Summary: "最后", Details: "最后详情"}); err != nil {
		t.Fatal(err)
	}
	if err := structured.UpdatePresentation(context.Background(), platform.StreamPresentation{Summary: "最终", Details: "最终详情"}); err != nil {
		t.Fatal(err)
	}
	if len(kit.streamElementIDs) != 1 {
		t.Fatalf("throttled streams=%#v, want only first presentation", kit.streamElementIDs)
	}
	s.flushPresentation()
	if len(kit.updateCardIDs) != 1 || len(kit.streamElementIDs) != 2 {
		t.Fatalf("updates=%d streams=%#v", len(kit.updateCardIDs), kit.streamElementIDs)
	}
	if kit.streamTexts[0] != "第一详情" || kit.streamTexts[1] != "最终详情" {
		t.Fatalf("texts=%#v", kit.streamTexts)
	}
	if kit.streamSeqs[1] <= kit.streamSeqs[0] {
		t.Fatalf("sequences=%#v", kit.streamSeqs)
	}
}

func TestTaskCardStructuredPresentationRetriesCompleteProgressAfterStreamingDisabled(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-summary-retry", streamErrors: []error{formatFeishuAPIError("cli_a", 200850, "disabled")}}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialPresentation: &platform.StreamPresentation{Summary: "初始", Details: "初始"}})
	if err != nil {
		t.Fatal(err)
	}
	err = stream.(platform.StructuredProgressStream).UpdatePresentation(context.Background(), platform.StreamPresentation{Summary: "摘要", Details: "详情"})
	if err != nil {
		t.Fatal(err)
	}
	if len(kit.streamingSeqs) < 2 || len(kit.streamElementIDs) != 2 {
		t.Fatalf("streaming=%#v elements=%#v", kit.streamingSeqs, kit.streamElementIDs)
	}
	if kit.streamElementIDs[0] != cardMainContentID || kit.streamElementIDs[1] != cardMainContentID {
		t.Fatalf("elements=%#v", kit.streamElementIDs)
	}
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

func TestFeishuTaskStreamPlacesThinkingAtBottomUntilTerminal(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeCardKitClient{cardID: "card-thinking"}
	reply := newReplierWithTaskCards(sender, "ou_user", cardKit, newTaskCardRegistry())
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex", InitialContent: "思考中.....",
	})
	if err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}
	created := decodeCardJSON(t, cardKit.createdCards[0])
	createdElements := created["body"].(map[string]any)["elements"].([]any)
	if len(createdElements) != 1 || createdElements[0].(map[string]any)["content"] != "思考中....." {
		t.Fatalf("created body=%#v, want only inline thinking content", created["body"])
	}

	const active = "我正在检查当前实现。\n\n思考中....."
	if err := stream.Update(context.Background(), active); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	activeCard := decodeCardJSON(t, cardKit.updateCards[len(cardKit.updateCards)-1])
	activeElements := activeCard["body"].(map[string]any)["elements"].([]any)
	if len(activeElements) != 1 || activeElements[0].(map[string]any)["content"] != active {
		t.Fatalf("active body=%#v, want reply followed by inline thinking", activeCard["body"])
	}
	reference, err := stream.(platform.DurableStreamReferenceExporter).DurableReference()
	if err != nil {
		t.Fatalf("DurableReference error: %v", err)
	}
	var referencePayload feishuStreamReferencePayload
	if err := json.Unmarshal(reference.Payload, &referencePayload); err != nil {
		t.Fatalf("decode durable reference: %v", err)
	}
	if referencePayload.Content != "我正在检查当前实现。" || strings.Contains(referencePayload.Content, "思考中.....") {
		t.Fatalf("durable content=%q, want process content without active thinking", referencePayload.Content)
	}

	const terminal = "我正在检查当前实现。"
	if err := stream.Complete(context.Background(), terminal); err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	doneCard := decodeCardJSON(t, cardKit.updateCards[len(cardKit.updateCards)-1])
	doneElements := doneCard["body"].(map[string]any)["elements"].([]any)
	if len(doneElements) != 1 || doneElements[0].(map[string]any)["content"] != terminal {
		t.Fatalf("done body=%#v, want preserved reply without redundant terminal text or thinking", doneCard["body"])
	}
	if doneCard["header"].(map[string]any)["template"] != "green" {
		t.Fatalf("done header=%#v, want green terminal header", doneCard["header"])
	}
	if strings.Contains(cardKit.updateCards[len(cardKit.updateCards)-1], "思考中.....") {
		t.Fatalf("terminal card must not retain active thinking indicator: %s", cardKit.updateCards[len(cardKit.updateCards)-1])
	}
}

func TestFeishuTaskStreamWithoutAgentReplyKeepsOnlyGreenTerminalHeader(t *testing.T) {
	cardKit := &fakeCardKitClient{cardID: "card-no-reply"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", cardKit, newTaskCardRegistry())
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex", InitialContent: platform.TaskStreamThinkingIndicator,
	})
	if err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}
	if err := stream.Complete(context.Background(), ""); err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	doneCard := decodeCardJSON(t, cardKit.updateCards[len(cardKit.updateCards)-1])
	body, ok := doneCard["body"].(map[string]any)
	if !ok || len(body["elements"].([]any)) != 0 {
		t.Fatalf("done body=%#v, want compact terminal card with required empty body", body)
	}
	if doneCard["header"].(map[string]any)["template"] != "green" {
		t.Fatalf("done header=%#v, want green terminal header", doneCard["header"])
	}
	if strings.Contains(cardKit.updateCards[len(cardKit.updateCards)-1], platform.TaskStreamThinkingIndicator) {
		t.Fatalf("terminal card must remove thinking without Agent reply: %s", cardKit.updateCards[len(cardKit.updateCards)-1])
	}
}

func TestFeishuTaskStreamWithoutProgressKeepsApprovalRecordsInCompactTerminal(t *testing.T) {
	cardKit := &fakeCardKitClient{cardID: "card-approval-only"}
	registry := newTaskCardRegistry()
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", cardKit, registry)
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex", InitialContent: platform.TaskStreamThinkingIndicator,
	})
	if err != nil {
		t.Fatalf("OpenStream error: %v", err)
	}
	registry.addApproval("card-approval-only", parsedCardAction{Choice: "accept", Label: "accept", Summary: "command: date"})
	if err := stream.Complete(context.Background(), ""); err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	doneCard := decodeCardJSON(t, cardKit.updateCards[len(cardKit.updateCards)-1])
	elements := doneCard["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 1 || !strings.Contains(elements[0].(map[string]any)["content"].(string), "command: date") {
		t.Fatalf("done body=%#v, want preserved approval without redundant terminal text", doneCard["body"])
	}
}

func TestOpenTaskStreamDoesNotBindCardBeforeStreamingIsEnabled(t *testing.T) {
	cardKit := &fakeCardKitClient{cardID: "card-failed", streamingErrors: []error{errors.New("streaming unavailable")}}
	reply := NewReplier(&fakeMessageSender{}, "ou_user", cardKit)

	if _, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialContent: "thinking"}); err == nil {
		t.Fatal("OpenStream error=nil, want streaming enable failure")
	}
	if got := reply.CurrentTaskCardID(); got != "" {
		t.Fatalf("current task card=%q, want previous binding unchanged", got)
	}
	if _, ok := reply.taskCards.snapshot("card-failed"); ok {
		t.Fatal("failed stream card was published to task card registry")
	}
}

func TestFeishuTaskStreamPreflightsCompleteCardJSONSize(t *testing.T) {
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "Codex", Content: "初始内容"})
	stream := &feishuStream{
		cardKit:   &fakeCardKitClient{},
		taskCards: registry,
		cardID:    "card-1",
		title:     "Codex",
		sequence:  1,
		now:       time.Now,
	}
	smallJSON, err := buildCardV2(cardOptions{Status: cardStatusThinking, Title: "Codex", Content: "短进展"})
	if err != nil {
		t.Fatal(err)
	}
	stream.cardJSONSoftLimitBytes = len([]byte(smallJSON)) + 8
	if err := stream.PreflightUpdate("短进展"); err != nil {
		t.Fatalf("short content preflight error: %v", err)
	}
	if err := stream.PreflightUpdate(strings.Repeat("长", 100)); !errors.Is(err, platform.ErrStreamContentTooLarge) {
		t.Fatalf("large content preflight error=%v, want ErrStreamContentTooLarge", err)
	}

	registry.addApproval("card-1", parsedCardAction{Choice: "allow", Label: strings.Repeat("审批", 40)})
	if err := stream.PreflightUpdate("短进展"); !errors.Is(err, platform.ErrStreamContentTooLarge) {
		t.Fatalf("preflight must include approval records, error=%v", err)
	}
}

func TestInitialStructuredTaskCardPreflightsExpandedDetails(t *testing.T) {
	cardKit := &fakeCardKitClient{cardID: "card-1"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", cardKit, newTaskCardRegistry())
	_, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex",
		InitialPresentation: &platform.StreamPresentation{
			Summary: "最新摘要",
			Preview: "最近五条",
			Details: strings.Repeat("x", feishuCardJSONSoftLimitBytes),
		},
	})
	if !errors.Is(err, platform.ErrStreamContentTooLarge) {
		t.Fatalf("OpenStream error=%v, want expanded details capacity failure", err)
	}
	if len(cardKit.createdCards) != 0 {
		t.Fatalf("created cards=%d, oversized complete timeline must fail before creation", len(cardKit.createdCards))
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
	elements := body["elements"].([]any)
	main := elements[0].(map[string]any)
	if got := main["content"]; got != "进展一" {
		t.Fatalf("final content=%q, want preserved latest delivered progress", got)
	}
	if strings.Contains(finalCard, "最终结果") || strings.Contains(finalCard, "待补发进展") {
		t.Fatalf("terminal card=%s, want no final answer or cancelled pending progress", finalCard)
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
	if got, _ := elements[1].(map[string]any)["content"].(string); !strings.Contains(got, "待补发进展") || !strings.Contains(got, "已在新位置继续展示") {
		t.Fatalf("content=%q, want pending progress and transfer notice", got)
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
	elements := body["elements"].([]any)
	main := elements[0].(map[string]any)
	if main["content"] != "done" {
		t.Fatalf("final content=%#v, want done", main["content"])
	}
}

func TestFeishuStreamStopUpdatesNeutralStoppedCard(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	stream := &feishuStream{cardKit: cardKit, cardID: "card-1", sequence: 4, throttle: cardkitThrottle, now: time.Now}

	if err := stream.Stop(context.Background(), "任务已按请求停止。"); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if len(cardKit.updateCards) != 1 {
		t.Fatalf("update cards=%d, want one stopped terminal update", len(cardKit.updateCards))
	}
	card := decodeCardJSON(t, cardKit.updateCards[0])
	header := card["header"].(map[string]any)
	if header["template"] != "grey" {
		t.Fatalf("template=%v, want grey", header["template"])
	}
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if got := elements[0].(map[string]any)["content"]; got != "**已停止**" {
		t.Fatalf("status=%q, want 已停止", got)
	}
	if got := elements[1].(map[string]any)["content"]; got != "任务已按请求停止。" {
		t.Fatalf("content=%q, want stopped explanation", got)
	}
	if strings.Contains(cardKit.updateCards[0], "执行失败") {
		t.Fatalf("stopped card must not contain failure wording: %s", cardKit.updateCards[0])
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
	if len(elements) != 2 {
		t.Fatalf("elements=%d, want preserved progress and approval record", len(elements))
	}
	if got := elements[0].(map[string]any)["content"]; got != "处理中" {
		t.Fatalf("content=%q, want preserved progress", got)
	}
	approval := elements[1].(map[string]any)
	if !strings.Contains(approval["content"].(string), "command: date") {
		t.Fatalf("approval content=%q, want approval record", approval["content"])
	}
	if strings.Contains(cardKit.updateCards[0], "最终结果") {
		t.Fatalf("terminal card must not contain final answer: %s", cardKit.updateCards[0])
	}
}

func TestFeishuStreamCompleteWithEmptyContentPreservesTaskCardProgress(t *testing.T) {
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
	if len(elements) != 1 || elements[0].(map[string]any)["content"] != "进展：任务仍在执行中，连接正常。" {
		t.Fatalf("done card body=%#v, want preserved progress without redundant status", card["body"])
	}
	header := card["header"].(map[string]any)
	if header["template"] != "green" {
		t.Fatalf("template=%v, want green", header["template"])
	}
}

func TestFeishuStructuredTerminalCollapsesPreviewWithoutThinkingIndicator(t *testing.T) {
	cardKit := &fakeIdempotentCardKitClient{fakeCardKitClient: fakeCardKitClient{cardID: "card-1"}}
	registry := newTaskCardRegistry()
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", cardKit, registry)
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex",
		InitialPresentation: &platform.StreamPresentation{
			Summary: "第六步",
			Preview: "第二步\n\n第三步\n\n第四步\n\n第五步\n\n第六步\n\n" + platform.TaskStreamThinkingIndicator,
			Details: "第一步\n\n第二步\n\n第三步\n\n第四步\n\n第五步\n\n第六步\n\n" + platform.TaskStreamThinkingIndicator,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := registry.setExpandedWithSequence("card-1", true); !ok {
		t.Fatal("expand task card")
	}
	if err := stream.Complete(context.Background(), "第一步\n\n第二步\n\n第三步\n\n第四步\n\n第五步\n\n第六步"); err != nil {
		t.Fatal(err)
	}

	opts, ok := registry.snapshot("card-1")
	if !ok || opts.Expanded || strings.Contains(opts.Preview, platform.TaskStreamThinkingIndicator) {
		t.Fatalf("terminal task card=%#v, want collapsed preview without active indicator", opts)
	}
	card := cardKit.updateCards[len(cardKit.updateCards)-1]
	if strings.Contains(card, platform.TaskStreamThinkingIndicator) {
		t.Fatalf("terminal card=%q, must remove active indicator", card)
	}
}

func TestFeishuStreamFailurePreservesTaskCardTimeline(t *testing.T) {
	cardKit := &fakeCardKitClient{}
	registry := newTaskCardRegistry()
	const timeline = "**执行进度**\n- ✅ 定位问题\n- • 运行测试"
	registry.record("card-1", cardOptions{Status: cardStatusThinking, Title: "Codex", Content: timeline})
	stream := &feishuStream{cardKit: cardKit, taskCards: registry, cardID: "card-1", title: "Codex", sequence: 4, throttle: cardkitThrottle, now: time.Now}

	if err := stream.Fail(context.Background(), "boom"); err != nil {
		t.Fatalf("Fail error: %v", err)
	}
	card := decodeCardJSON(t, cardKit.updateCards[0])
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if len(elements) != 2 || elements[0].(map[string]any)["content"] != "**执行失败**" ||
		elements[1].(map[string]any)["content"] != timeline {
		t.Fatalf("failed card body=%#v, want status and preserved timeline", card["body"])
	}
	if strings.Contains(cardKit.updateCards[0], "boom") {
		t.Fatalf("failure result must stay outside task card: %s", cardKit.updateCards[0])
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

func TestTaskCardApprovalRefreshesDurableReferenceAfterStreamEnable(t *testing.T) {
	cardKit := &fakeCardKitClient{cardID: "card-1"}
	registry := newTaskCardRegistry()
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", cardKit, registry)
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{Title: "Codex", InitialContent: "处理中"})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	notifier, ok := stream.(interface{ SetDurableReferenceChangeHandler(func()) })
	if !ok {
		t.Fatalf("stream=%T, want durable reference change notifier", stream)
	}
	notified := 0
	notifier.SetDurableReferenceChangeHandler(func() { notified++ })

	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.cardKit = cardKit
	adapter.taskCards = registry
	if !adapter.updateTaskCardWithApproval(context.Background(), parsedCardAction{
		TaskCard: "card-1", Choice: "accept", Label: "允许本次", Summary: "command: date",
	}) {
		t.Fatal("approval update should update task card")
	}
	if notified != 1 {
		t.Fatalf("durable reference notifications=%d, want 1", notified)
	}
	if len(cardKit.streamingSeqs) != 1 || len(cardKit.updateSeqs) != 1 || cardKit.updateSeqs[0] <= cardKit.streamingSeqs[0] {
		t.Fatalf("streaming seqs=%#v update seqs=%#v, approval must advance past stream enable", cardKit.streamingSeqs, cardKit.updateSeqs)
	}
	reference, err := stream.(platform.DurableStreamReferenceExporter).DurableReference()
	if err != nil {
		t.Fatalf("DurableReference: %v", err)
	}
	var payload feishuStreamReferencePayload
	if err := json.Unmarshal(reference.Payload, &payload); err != nil {
		t.Fatalf("decode durable reference: %v", err)
	}
	if payload.Sequence != cardKit.updateSeqs[0] || len(payload.Approvals) != 1 || !strings.Contains(payload.Approvals[0], "command: date") {
		t.Fatalf("payload=%#v, want latest approval and sequence", payload)
	}
}

func TestFeishuStructuredDurableReferencePreservesPresentation(t *testing.T) {
	cardKit := &fakeCardKitClient{cardID: "card-1"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", cardKit, newTaskCardRegistry())
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex", InitialPresentation: &platform.StreamPresentation{Summary: "开始", Preview: "初始预览", Details: "初始详情"},
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	structured := stream.(platform.StructuredProgressStream)
	s := stream.(*feishuStream)
	s.throttle = time.Hour
	t.Cleanup(func() {
		s.mu.Lock()
		s.cancelPendingPresentation()
		s.mu.Unlock()
	})
	if err := structured.UpdatePresentation(context.Background(), platform.StreamPresentation{Summary: "第一步", Preview: "第一步预览", Details: "第一步详情"}); err != nil {
		t.Fatalf("first presentation: %v", err)
	}
	if _, _, _, ok := reply.taskCards.setExpandedWithSequence("card-1", true); !ok {
		t.Fatal("expand task card")
	}
	if err := structured.UpdatePresentation(context.Background(), platform.StreamPresentation{Summary: "最新摘要", Preview: "最新预览\n\n思考中.....", Details: "最新详情\n\n思考中....."}); err != nil {
		t.Fatalf("pending presentation: %v", err)
	}
	reference, err := stream.(platform.DurableStreamReferenceExporter).DurableReference()
	if err != nil {
		t.Fatalf("DurableReference: %v", err)
	}
	var payload feishuStreamReferencePayload
	if err := json.Unmarshal(reference.Payload, &payload); err != nil {
		t.Fatalf("decode durable reference: %v", err)
	}
	if payload.Summary != "最新摘要" || payload.Preview != "最新预览" || payload.Details != "最新详情" ||
		payload.Content != payload.Details || !payload.Collapsible || !payload.Expanded {
		t.Fatalf("payload=%#v", payload)
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

func TestFeishuPrepareSupersedeFromReferencePreservesHiddenProgressAndExpandControl(t *testing.T) {
	payload, err := json.Marshal(feishuStreamReferencePayload{
		CardID: "card-1", Title: "Codex · project-a", Sequence: 7,
		Content: "旧兼容进度", Summary: "已完成代码检查", Details: "1. 已读取实现\n2. 已补充测试",
		Collapsible: true, Approvals: []string{"允许本次：command: date"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reply := NewReplier(nil, "oc_chat", &fakeIdempotentCardKitClient{})
	checkpoint, err := reply.PrepareSupersedeFromReference(platform.DurableStreamReference{
		Kind: feishuStreamReferenceKind, Payload: payload,
	}, "已在下方新卡继续展示。", "reanchor-1")
	if err != nil {
		t.Fatalf("PrepareSupersedeFromReference: %v", err)
	}
	if checkpoint.Kind != feishuSupersedeCheckpointKind {
		t.Fatalf("kind=%q", checkpoint.Kind)
	}
	var op feishuStreamTerminalOp
	if err := json.Unmarshal(checkpoint.Payload, &op); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	if op.DisableSeq != 8 || op.UpdateSeq != 9 || op.DisableOperation == "" || op.UpdateOperation == "" {
		t.Fatalf("operation=%#v", op)
	}

	card := decodeCardJSON(t, op.CardJSON)
	elements := card["body"].(map[string]any)["elements"].([]any)
	var status, approval string
	foundExpand := false
	for _, raw := range elements {
		element := raw.(map[string]any)
		switch element["element_id"] {
		case "status":
			status, _ = element["content"].(string)
		case cardProgressExpandID:
			foundExpand = true
			behaviors := element["behaviors"].([]any)
			value := behaviors[0].(map[string]any)["value"].(map[string]any)
			if value["task_card_id"] != "card-1" {
				t.Fatalf("button value=%#v, want superseded card id", value)
			}
		case "approval_records":
			approval, _ = element["content"].(string)
		}
	}
	if status != "**已转移**" || !foundExpand {
		t.Fatalf("status=%q expand=%v", status, foundExpand)
	}
	if op.TaskCard == nil || op.TaskCard.Summary != "已完成代码检查" ||
		!strings.Contains(op.TaskCard.Content, "1. 已读取实现") ||
		!strings.Contains(op.TaskCard.Content, "已在下方新卡继续展示。") {
		t.Fatalf("task card=%#v, want preserved hidden progress and transfer notice", op.TaskCard)
	}
	if !strings.Contains(approval, "command: date") {
		t.Fatalf("approval=%q", approval)
	}
}

func TestFeishuDeliverSupersedeCheckpointIsIdempotent(t *testing.T) {
	payload, err := json.Marshal(feishuStreamReferencePayload{
		CardID: "card-1", Title: "Codex", Sequence: 4, Content: "当前进度",
	})
	if err != nil {
		t.Fatal(err)
	}
	cardKit := &fakeIdempotentCardKitClient{}
	reply := NewReplier(nil, "oc_chat", cardKit)
	checkpoint, err := reply.PrepareSupersedeFromReference(platform.DurableStreamReference{
		Kind: feishuStreamReferenceKind, Payload: payload,
	}, "已在下方新卡继续展示。", "reanchor-1")
	if err != nil {
		t.Fatalf("PrepareSupersedeFromReference: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := reply.DeliverSupersede(context.Background(), checkpoint); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if len(cardKit.streamOperations) != 2 || cardKit.streamOperations[0] == "" || cardKit.streamOperations[0] != cardKit.streamOperations[1] {
		t.Fatalf("stream operations=%#v", cardKit.streamOperations)
	}
	if len(cardKit.updateOperations) != 2 || cardKit.updateOperations[0] == "" || cardKit.updateOperations[0] != cardKit.updateOperations[1] {
		t.Fatalf("update operations=%#v", cardKit.updateOperations)
	}
	if len(cardKit.streamingSeqs) != 1 || len(cardKit.updateSeqs) != 1 {
		t.Fatalf("effective streaming=%#v updates=%#v", cardKit.streamingSeqs, cardKit.updateSeqs)
	}
	if len(cardKit.destroyed) != 0 {
		t.Fatalf("supersede must preserve historical card: destroyed=%#v", cardKit.destroyed)
	}
}

func TestFeishuSupersedeCheckpointRestoresProgressControlStateAfterRestart(t *testing.T) {
	payload, err := json.Marshal(feishuStreamReferencePayload{
		CardID: "card-1", Title: "Codex", Sequence: 4,
		Summary: "检查实现", Details: "第一步\n\n第二步", Collapsible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := prepareFeishuSupersedeFromReference(platform.DurableStreamReference{
		Kind: feishuStreamReferenceKind, Payload: payload,
	}, "已在新位置继续展示", "supersede-restart-1")
	if err != nil {
		t.Fatal(err)
	}
	cardKit := &fakeIdempotentCardKitClient{}
	afterCards := newTaskCardRegistry()
	afterRestart := newReplierWithTaskCards(nil, "ou_user", cardKit, afterCards)
	if err := afterRestart.DeliverSupersede(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	opts, ok := afterCards.snapshot("card-1")
	if !ok || opts.Status != cardStatusSuperseded ||
		!strings.Contains(opts.Content, "第一步") || !strings.Contains(opts.Content, "已在新位置继续展示") || opts.Expanded {
		t.Fatalf("restored superseded task card=%#v ok=%v", opts, ok)
	}
}

func TestCollapsedTaskCardPreflightChecksCompleteHiddenProgress(t *testing.T) {
	registry := newTaskCardRegistry()
	registry.record("card-1", cardOptions{
		Status: cardStatusStreaming, Title: "Codex", Summary: "摘要", Content: "旧进度",
		Collapsible: true, Expanded: false,
	})
	stream := &feishuStream{
		taskCards: registry, cardID: "card-1", title: "Codex",
		collapsible: true, cardJSONSoftLimitBytes: 1_000,
	}
	err := stream.PreflightUpdate(strings.Repeat("完整进度", 1_000))
	if !errors.Is(err, platform.ErrStreamContentTooLarge) {
		t.Fatalf("PreflightUpdate error=%v, want complete hidden progress capacity failure", err)
	}
}

func TestFeishuTerminalCheckpointRestoresProgressControlStateAfterRestart(t *testing.T) {
	cardKit := &fakeIdempotentCardKitClient{fakeCardKitClient: fakeCardKitClient{cardID: "card-1"}}
	beforeRestart := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", cardKit, newTaskCardRegistry())
	stream, err := beforeRestart.OpenStream(context.Background(), platform.StreamOptions{
		Title:               "Codex",
		InitialPresentation: &platform.StreamPresentation{Summary: "检查实现", Preview: "第二步", Details: "第一步\n\n第二步"},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stream.(platform.StatefulDurableTerminalStream).PrepareTerminalWithState("", platform.StreamTerminalCompleted)
	if err != nil {
		t.Fatal(err)
	}

	afterCards := newTaskCardRegistry()
	afterRestart := newReplierWithTaskCards(nil, "ou_user", cardKit, afterCards)
	if err := afterRestart.DeliverTerminal(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	opts, ok := afterCards.snapshot("card-1")
	if !ok || opts.Status != cardStatusDone || opts.Preview != "第二步" || opts.Content != "第一步\n\n第二步" || opts.Expanded {
		t.Fatalf("restored task card=%#v ok=%v", opts, ok)
	}
	card := decodeCardJSON(t, cardKit.updateCards[len(cardKit.updateCards)-1])
	elements := card["body"].(map[string]any)["elements"].([]any)
	if len(elements) < 2 || elements[0].(map[string]any)["element_id"] != cardMainContentID ||
		elements[0].(map[string]any)["content"] != "第二步" ||
		elements[len(elements)-1].(map[string]any)["element_id"] != cardProgressExpandID {
		t.Fatalf("terminal elements=%#v, want collapsed preview and expand control", elements)
	}
}

func TestFeishuDeliverPreparedSupersedeCancelsPendingState(t *testing.T) {
	cardKit := &fakeIdempotentCardKitClient{}
	registry := newTaskCardRegistry()
	registry.recordWithSequence("card-1", cardOptions{
		Status: cardStatusThinking, Title: "Codex", Summary: "摘要", Content: "详情",
		Collapsible: true, Expanded: true,
	}, 3)
	stream := &feishuStream{
		cardKit: cardKit, taskCards: registry, cardID: "card-1", title: "Codex", sequence: 3,
		lastSummary: "摘要", lastContent: "详情", collapsible: true, now: time.Now,
		hasPending: true, pendingText: "待补发文本", pendingTimer: time.AfterFunc(time.Hour, func() {}),
		hasPendingPresentation: true, pendingPresentation: platform.StreamPresentation{Summary: "待补发摘要", Details: "待补发详情"},
		presentationTimer: time.AfterFunc(time.Hour, func() {}),
	}
	notified := 0
	stream.SetDurableReferenceChangeHandler(func() { notified++ })
	reference, err := stream.DurableReference()
	if err != nil {
		t.Fatalf("DurableReference: %v", err)
	}
	checkpoint, err := prepareFeishuSupersedeFromReference(reference, "已在下方新卡继续展示。", "reanchor-1")
	if err != nil {
		t.Fatalf("prepare checkpoint: %v", err)
	}
	if err := stream.DeliverPreparedSupersede(context.Background(), checkpoint); err != nil {
		t.Fatalf("DeliverPreparedSupersede: %v", err)
	}
	stream.mu.Lock()
	closed := stream.closed
	hasPending := stream.hasPending || stream.pendingTimer != nil || stream.hasPendingPresentation || stream.presentationTimer != nil
	stream.mu.Unlock()
	if !closed || hasPending {
		t.Fatalf("closed=%v pending=%v", closed, hasPending)
	}
	registry.addApproval("card-1", parsedCardAction{Choice: "accept", Label: "允许本次", Summary: "command: date"})
	if notified != 0 {
		t.Fatalf("recovery callback fired after supersede: %d", notified)
	}
}

func TestFeishuStreamReferenceCompletesOriginalCardAfterRestart(t *testing.T) {
	sender := &fakeMessageSender{}
	cardKit := &fakeIdempotentCardKitClient{}
	beforeCards := newTaskCardRegistry()
	beforeRestart := newReplierWithTaskCards(sender, "oc_chat", cardKit, beforeCards)
	stream, err := beforeRestart.OpenStream(context.Background(), platform.StreamOptions{
		Title: "WeClaw · 重启", InitialContent: "正在重启",
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	exporter, ok := stream.(platform.DurableStreamReferenceExporter)
	if !ok {
		t.Fatalf("stream=%T, want DurableStreamReferenceExporter", stream)
	}
	if err := stream.Update(context.Background(), "已完成验证，正在收尾"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	beforeCards.addApproval("card-1", parsedCardAction{Choice: "accept", Label: "允许本次", Summary: "command: date"})
	reference, err := exporter.DurableReference()
	if err != nil {
		t.Fatalf("DurableReference: %v", err)
	}

	afterRestart := newReplierWithTaskCards(nil, "oc_chat", cardKit, newTaskCardRegistry())
	checkpoint, err := afterRestart.PrepareTerminalFromReference(reference, "WeClaw 已重启完成。\n版本：v0.1.test", false)
	if err != nil {
		t.Fatalf("PrepareTerminalFromReference: %v", err)
	}
	if err := afterRestart.DeliverTerminal(context.Background(), checkpoint); err != nil {
		t.Fatalf("DeliverTerminal: %v", err)
	}

	if len(cardKit.updateCardIDs) < 2 || cardKit.updateCardIDs[len(cardKit.updateCardIDs)-1] != "card-1" {
		t.Fatalf("updated card IDs=%#v, want original card", cardKit.updateCardIDs)
	}
	if len(cardKit.streamingSeqs) != 2 || cardKit.streamingSeqs[0] != 1 ||
		len(cardKit.updateSeqs) < 2 || cardKit.streamingSeqs[1] <= cardKit.updateSeqs[len(cardKit.updateSeqs)-2] {
		t.Fatalf("streaming seqs=%#v update seqs=%#v, terminal disable must follow progress and approval updates", cardKit.streamingSeqs, cardKit.updateSeqs)
	}
	if cardKit.updateSeqs[len(cardKit.updateSeqs)-1] <= cardKit.streamingSeqs[1] {
		t.Fatalf("streaming seqs=%#v update seqs=%#v, terminal update must follow disable", cardKit.streamingSeqs, cardKit.updateSeqs)
	}
	terminalCard := cardKit.updateCards[len(cardKit.updateCards)-1]
	if strings.Contains(terminalCard, "**已完成**") || !strings.Contains(terminalCard, "已完成验证，正在收尾") ||
		!strings.Contains(terminalCard, "command: date") || strings.Contains(terminalCard, "v0.1.test") {
		t.Fatalf("terminal card=%q, want preserved progress and approval without final answer", terminalCard)
	}
}

func TestFeishuStreamReferenceStopsInterruptedOriginalCardAfterRestart(t *testing.T) {
	cardKit := &fakeIdempotentCardKitClient{}
	beforeRestart := NewReplier(&fakeMessageSender{}, "oc_chat", cardKit)
	stream, err := beforeRestart.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Claude · jumpserver", InitialContent: platform.TaskStreamThinkingIndicator,
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	reference, err := stream.(platform.DurableStreamReferenceExporter).DurableReference()
	if err != nil {
		t.Fatalf("DurableReference: %v", err)
	}
	afterRestart := NewReplier(nil, "oc_chat", cardKit)
	checkpoint, err := afterRestart.PrepareTerminalFromReferenceWithState(
		reference, "任务已中断。WeClaw 服务在任务执行期间发生重启。", platform.StreamTerminalStopped,
	)
	if err != nil {
		t.Fatalf("PrepareTerminalFromReferenceWithState: %v", err)
	}
	if err := afterRestart.DeliverTerminal(context.Background(), checkpoint); err != nil {
		t.Fatalf("DeliverTerminal: %v", err)
	}
	if len(cardKit.updateCardIDs) != 1 || cardKit.updateCardIDs[0] != "card-1" {
		t.Fatalf("updated card IDs=%#v, want original card", cardKit.updateCardIDs)
	}
	if card := cardKit.updateCards[0]; !strings.Contains(card, "已停止") || strings.Contains(card, "思考中") ||
		strings.Contains(card, "未产生结构化进度") || strings.Contains(card, "任务已中断") {
		t.Fatalf("terminal card=%q, want compact stopped state without synthetic progress", card)
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
