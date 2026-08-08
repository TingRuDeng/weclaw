package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

type rolloverTestReplier struct {
	mu           sync.Mutex
	limit        int
	streams      []*rolloverTestStream
	options      []platform.StreamOptions
	route        platform.DeliveryRoute
	cardID       string
	supersedeErr error
}

func (r *rolloverTestReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true, Streaming: true}
}
func (r *rolloverTestReplier) SendText(context.Context, string) error  { return nil }
func (r *rolloverTestReplier) SendImage(context.Context, string) error { return nil }
func (r *rolloverTestReplier) SendFile(context.Context, string) error  { return nil }
func (r *rolloverTestReplier) Typing(context.Context, bool) error      { return nil }
func (r *rolloverTestReplier) AskChoices(context.Context, string, []platform.Choice) error {
	return nil
}
func (r *rolloverTestReplier) OpenStream(_ context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cardID := "card-" + fmt.Sprint(len(r.streams)+1)
	stream := &rolloverTestStream{limit: r.limit, cardID: cardID, deliverErr: r.supersedeErr}
	r.streams = append(r.streams, stream)
	r.options = append(r.options, opts)
	r.cardID = cardID
	return stream, nil
}

func (r *rolloverTestReplier) CurrentTaskCardID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cardID
}

func (r *rolloverTestReplier) BindTaskCard(cardID string) {
	r.mu.Lock()
	r.cardID = cardID
	r.mu.Unlock()
}

func (r *rolloverTestReplier) DeliveryRoute() platform.DeliveryRoute { return r.route }

func (r *rolloverTestReplier) PrepareSupersedeFromReference(_ platform.DurableStreamReference, notice string, operationID string) (platform.SupersedeCheckpoint, error) {
	payload, err := json.Marshal(map[string]string{"notice": notice, "operation_id": operationID})
	return platform.SupersedeCheckpoint{Kind: "rollover.test.supersede.v1", Payload: payload}, err
}

func (r *rolloverTestReplier) streamsSnapshot() []*rolloverTestStream {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*rolloverTestStream(nil), r.streams...)
}

type rolloverTestStream struct {
	mu                 sync.Mutex
	limit              int
	updates            []string
	superseded         []string
	completed          []string
	cardID             string
	preparedSupersedes int
	fallbackSupersedes int
	deliverErr         error
}

func (s *rolloverTestStream) PreflightUpdate(content string) error {
	if s.limit > 0 && len([]byte(content)) > s.limit {
		return platform.ErrStreamContentTooLarge
	}
	return nil
}
func (s *rolloverTestStream) Update(_ context.Context, content string) error {
	if err := s.PreflightUpdate(content); err != nil {
		return err
	}
	s.mu.Lock()
	s.updates = append(s.updates, content)
	s.mu.Unlock()
	return nil
}
func (s *rolloverTestStream) Complete(_ context.Context, content string) error {
	s.mu.Lock()
	s.completed = append(s.completed, content)
	s.mu.Unlock()
	return nil
}
func (s *rolloverTestStream) Fail(ctx context.Context, content string) error {
	return s.Complete(ctx, content)
}
func (s *rolloverTestStream) Supersede(_ context.Context, content string) error {
	s.mu.Lock()
	s.superseded = append(s.superseded, content)
	s.fallbackSupersedes++
	s.mu.Unlock()
	return nil
}

func (s *rolloverTestStream) DeliverPreparedSupersede(_ context.Context, checkpoint platform.SupersedeCheckpoint) error {
	var payload struct {
		Notice string `json:"notice"`
	}
	if err := json.Unmarshal(checkpoint.Payload, &payload); err != nil {
		return err
	}
	s.mu.Lock()
	s.superseded = append(s.superseded, payload.Notice)
	s.preparedSupersedes++
	err := s.deliverErr
	s.mu.Unlock()
	return err
}

func (s *rolloverTestStream) DurableReference() (platform.DurableStreamReference, error) {
	payload, err := json.Marshal(map[string]string{"card_id": s.cardID})
	return platform.DurableStreamReference{Kind: "rollover.test.stream.v1", Payload: payload}, err
}

func (s *rolloverTestStream) updateCounts() (updates int, superseded int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.updates), len(s.superseded)
}

func (s *rolloverTestStream) supersedeDeliveryCounts() (prepared int, fallback int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preparedSupersedes, s.fallbackSupersedes
}

func TestProgressRolloverUsesDurableReanchorTransaction(t *testing.T) {
	h := NewHandler(nil, nil)
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	outbox, _ := attachTestTerminalOutbox(t, h)
	reply := &rolloverTestReplier{
		limit: 350, route: route, supersedeErr: errors.New("temporary CardKit failure"),
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/project", "执行任务", cfg,
	)
	first := []agent.ProgressEvent{
		{ID: "a", Kind: agent.ProgressKindFile, State: agent.ProgressStateCompleted, Text: "定位问题"},
		{ID: "b", Kind: agent.ProgressKindCommand, State: agent.ProgressStateRunning, Text: "运行测试"},
	}
	firstCard, _ := renderTaskProgressTimeline(first, "")
	session.onTaskProgress(taskProgressUpdate{latest: "定位问题", card: firstCard, timeline: true, timelineItems: first})
	waitForRolloverCondition(t, func() bool {
		streams := reply.streamsSnapshot()
		if len(streams) != 1 {
			return false
		}
		updates, _ := streams[0].updateCounts()
		return updates == 1
	})

	second := append(append([]agent.ProgressEvent(nil), first...), agent.ProgressEvent{
		ID: "c", Kind: agent.ProgressKindFile, State: agent.ProgressStateCompleted, Text: strings.Repeat("新增进展", 24),
	})
	secondCard, _ := renderTaskProgressTimeline(second, "")
	session.onTaskProgress(taskProgressUpdate{latest: second[2].Text, card: secondCard, timeline: true, timelineItems: second})
	waitForRolloverCondition(t, func() bool {
		streams := reply.streamsSnapshot()
		if len(streams) != 2 {
			return false
		}
		prepared, _ := streams[0].supersedeDeliveryCounts()
		return prepared == 1
	})

	streams := reply.streamsSnapshot()
	prepared, fallback := streams[0].supersedeDeliveryCounts()
	if prepared != 1 || fallback != 0 {
		t.Fatalf("supersede deliveries prepared=%d fallback=%d", prepared, fallback)
	}
	entry := outbox.entryLocked(session.recoveryReservation)
	if entry == nil || entry.Stream == nil || !strings.Contains(string(entry.Stream.Payload), "card-2") || len(entry.PendingSupersedes) != 1 {
		t.Fatalf("rollover transaction was not durably retained: %#v", entry)
	}
	if !finish("最终结果", false) {
		t.Fatal("latest continuation card should consume final result")
	}
}

func TestStructuredProgressAutomaticallyContinuesOnAnotherCardBeforeOverflow(t *testing.T) {
	reply := &rolloverTestReplier{limit: 350}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, finish, session := NewHandler(nil, nil).startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "claude", "/workspace/project", "执行任务", cfg,
	)
	first := []agent.ProgressEvent{
		{ID: "a", Kind: agent.ProgressKindCommand, State: agent.ProgressStateCompleted, Text: "定位问题"},
		{ID: "b", Kind: agent.ProgressKindCommand, State: agent.ProgressStateRunning, Text: "运行测试"},
	}
	firstCard, _ := renderTaskProgressTimeline(first, "")
	currentExplanation := "正在运行回归测试。"
	firstCard = appendTaskCurrentExplanation(firstCard, currentExplanation)
	session.onTaskProgress(taskProgressUpdate{
		latest: "运行测试", card: firstCard, timeline: true,
		currentExplanation: currentExplanation, timelineItems: first,
	})
	waitForRolloverCondition(t, func() bool {
		streams := reply.streamsSnapshot()
		if len(streams) != 1 {
			return false
		}
		updates, _ := streams[0].updateCounts()
		return updates == 1
	})

	second := append(append([]agent.ProgressEvent(nil), first...),
		agent.ProgressEvent{ID: "c", Kind: agent.ProgressKindFile, State: agent.ProgressStateCompleted, Text: strings.Repeat("新增进展", 20)},
	)
	secondCard, _ := renderTaskProgressTimeline(second, "")
	secondCard = appendTaskCurrentExplanation(secondCard, currentExplanation)
	session.onTaskProgress(taskProgressUpdate{
		latest: second[len(second)-1].Text, card: secondCard, timeline: true,
		currentExplanation: currentExplanation, timelineItems: second,
	})
	waitForRolloverCondition(t, func() bool {
		streams := reply.streamsSnapshot()
		if len(streams) != 2 {
			return false
		}
		_, superseded := streams[0].updateCounts()
		return superseded == 1
	})

	reply.mu.Lock()
	oldStream, newStream := reply.streams[0], reply.streams[1]
	newInitial := reply.options[1].InitialContent
	newTitle := reply.options[1].Title
	reply.mu.Unlock()
	oldStream.mu.Lock()
	oldNotice := oldStream.superseded[0]
	oldStream.mu.Unlock()
	if !strings.Contains(oldNotice, "定位问题") || !strings.Contains(oldNotice, "后续进度见第 2 张卡片") {
		t.Fatalf("old card=%q, want preserved progress and continuation notice", oldNotice)
	}
	if strings.Contains(newInitial, "定位问题") || !strings.Contains(newInitial, "新增进展") ||
		!strings.Contains(newInitial, "**当前说明**") || !strings.Contains(newInitial, currentExplanation) ||
		!strings.Contains(newTitle, "进度 2") {
		t.Fatalf("new title=%q initial=%q, want only next segment", newTitle, newInitial)
	}
	if got := reply.CurrentTaskCardID(); got != "card-2" {
		t.Fatalf("current task card=%q, want card-2", got)
	}
	slidWindow := append(append([]agent.ProgressEvent(nil), second[1:]...),
		agent.ProgressEvent{ID: "d", Kind: agent.ProgressKindCommand, State: agent.ProgressStateRunning, Text: "继续验证"},
	)
	slidCard, _ := renderTaskProgressTimeline(slidWindow, "")
	slidCard = appendTaskCurrentExplanation(slidCard, currentExplanation)
	session.onTaskProgress(taskProgressUpdate{
		latest: "继续验证", card: slidCard, timeline: true, timelineItems: slidWindow,
		currentExplanation: currentExplanation,
	})
	waitForRolloverCondition(t, func() bool {
		updates, _ := newStream.updateCounts()
		return updates == 1
	})
	newStream.mu.Lock()
	continuedContent := newStream.updates[0]
	newStream.mu.Unlock()
	if !strings.Contains(continuedContent, "新增进展") || !strings.Contains(continuedContent, "继续验证") ||
		!strings.Contains(continuedContent, currentExplanation) || strings.Contains(continuedContent, "运行测试") {
		t.Fatalf("continued content=%q, want current segment history after bounded timeline slides", continuedContent)
	}
	if !finish("最终结果", false) {
		t.Fatal("latest continuation card should consume final result")
	}
	newStream.mu.Lock()
	defer newStream.mu.Unlock()
	if len(newStream.completed) != 1 || newStream.completed[0] != "最终结果" {
		t.Fatalf("new completed=%#v", newStream.completed)
	}
}

func TestCodexCommentaryAutomaticallyContinuesWithoutLosingEarlierMessages(t *testing.T) {
	reply := &rolloverTestReplier{limit: 220}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, finish, session := NewHandler(nil, nil).startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/project", "执行任务", cfg,
	)
	task, _ := newActiveAgentTask(context.Background(), activeTaskMeta{owner: "user-1", agentName: "codex"})
	task.setProgressTimelineLimit(cfg.EffectiveStreamTimelineLimit())
	firstText := strings.Repeat("第一段说明。", 7)
	firstUpdate, ok := task.recordProgressUpdate(time.Now(), agent.ProgressEvent{
		ID: "agent-message:first", Kind: agent.ProgressKindCommentary,
		State: agent.ProgressStateCompleted, Sequence: 1, Text: firstText,
	})
	if !ok {
		t.Fatal("first commentary was not recorded")
	}
	session.onTaskProgress(firstUpdate)
	waitForRolloverCondition(t, func() bool {
		streams := reply.streamsSnapshot()
		if len(streams) != 1 {
			return false
		}
		updates, _ := streams[0].updateCounts()
		return updates == 1
	})

	secondText := strings.Repeat("第二段说明。", 7)
	secondUpdate, ok := task.recordProgressUpdate(time.Now().Add(time.Second), agent.ProgressEvent{
		ID: "agent-message:second", Kind: agent.ProgressKindCommentary,
		State: agent.ProgressStateCompleted, Sequence: 2, Text: secondText,
	})
	if !ok {
		t.Fatal("second commentary was not recorded")
	}
	session.onTaskProgress(secondUpdate)
	waitForRolloverCondition(t, func() bool {
		streams := reply.streamsSnapshot()
		if len(streams) != 2 {
			return false
		}
		_, superseded := streams[0].updateCounts()
		return superseded == 1
	})

	reply.mu.Lock()
	oldStream, newStream := reply.streams[0], reply.streams[1]
	newInitial := reply.options[1].InitialContent
	newTitle := reply.options[1].Title
	reply.mu.Unlock()
	oldStream.mu.Lock()
	oldNotice := oldStream.superseded[0]
	oldStream.mu.Unlock()
	if !strings.Contains(oldNotice, firstText) || strings.Contains(oldNotice, secondText) ||
		!strings.Contains(oldNotice, "后续进度见第 2 张卡片") {
		t.Fatalf("old card=%q, want first commentary and continuation notice", oldNotice)
	}
	if strings.Contains(newInitial, firstText) || !strings.Contains(newInitial, secondText) ||
		!strings.Contains(newTitle, "进度 2") {
		t.Fatalf("new title=%q initial=%q, want second commentary only", newTitle, newInitial)
	}
	if !finish("最终结果", false) {
		t.Fatal("latest continuation card should consume final result")
	}
	newStream.mu.Lock()
	defer newStream.mu.Unlock()
	if len(newStream.completed) != 1 || newStream.completed[0] != "最终结果" {
		t.Fatalf("new completed=%#v", newStream.completed)
	}
}

func TestStructuredProgressContinuationRefreshesDurableRecoveryReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	recoveryReply := newOutboxTestReplier(route)
	registry := newOutboxTestRegistry(route, recoveryReply)
	h := NewHandler(nil, nil)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	if err := h.StartTerminalOutbox(workerCtx, registry, path); err != nil {
		t.Fatalf("StartTerminalOutbox: %v", err)
	}
	reply := &rolloverTestReplier{limit: 300, route: route}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, _, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/project", "执行任务", cfg,
	)
	first := []agent.ProgressEvent{
		{ID: "agent-message:first", Kind: agent.ProgressKindCommentary, State: agent.ProgressStateCompleted, Text: "我先检查当前实现。"},
		{ID: "a", Kind: agent.ProgressKindFile, State: agent.ProgressStateCompleted, Text: "定位问题"},
	}
	firstCard, _ := renderTaskProgressTimeline(first, "")
	session.onTaskProgress(taskProgressUpdate{latest: "定位问题", card: firstCard, timeline: true, timelineItems: first})
	waitForRolloverCondition(t, func() bool {
		streams := reply.streamsSnapshot()
		if len(streams) != 1 {
			return false
		}
		updates, _ := streams[0].updateCounts()
		return updates == 1
	})
	second := append(append([]agent.ProgressEvent(nil), first...),
		agent.ProgressEvent{ID: "b", Kind: agent.ProgressKindFile, State: agent.ProgressStateCompleted, Text: strings.Repeat("新增进展", 24)},
	)
	secondCard, _ := renderTaskProgressTimeline(second, "")
	session.onTaskProgress(taskProgressUpdate{latest: second[1].Text, card: secondCard, timeline: true, timelineItems: second})
	waitForRolloverCondition(t, func() bool {
		streams := reply.streamsSnapshot()
		if len(streams) != 2 {
			return false
		}
		_, superseded := streams[0].updateCounts()
		return superseded == 1
	})

	stopWorker()
	session.stopBackground()
	persisted, err := loadTerminalOutbox(path)
	if err != nil || len(persisted) != 1 {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	restarted, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.attempt(context.Background(), persisted[0].ID, nil); err != nil {
		t.Fatalf("recover continued card: %v", err)
	}
	recoveryReply.mu.Lock()
	defer recoveryReply.mu.Unlock()
	if len(recoveryReply.recoveredReferences) != 1 || !strings.Contains(string(recoveryReply.recoveredReferences[0].Payload), "card-2") {
		t.Fatalf("recovered references=%#v, want latest continuation card", recoveryReply.recoveredReferences)
	}
}

func waitForRolloverCondition(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for progress rollover")
}

var _ platform.StreamContentPreflighter = (*rolloverTestStream)(nil)
