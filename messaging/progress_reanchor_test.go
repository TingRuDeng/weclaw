package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

type reanchorTestReplier struct {
	mu          sync.Mutex
	stream      *reanchorTestStream
	openCalls   int
	lastOptions platform.StreamOptions
	cardID      string
	route       platform.DeliveryRoute
	openEvent   string
	events      *reanchorEventRecorder
	onBind      func(string)
}

func newReanchorTestReplier() *reanchorTestReplier {
	return &reanchorTestReplier{stream: &reanchorTestStream{}}
}

func (r *reanchorTestReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true, Streaming: true}
}

func (r *reanchorTestReplier) SendText(context.Context, string) error  { return nil }
func (r *reanchorTestReplier) SendImage(context.Context, string) error { return nil }
func (r *reanchorTestReplier) SendFile(context.Context, string) error  { return nil }
func (r *reanchorTestReplier) Typing(context.Context, bool) error      { return nil }
func (r *reanchorTestReplier) AskChoices(context.Context, string, []platform.Choice) error {
	return nil
}

func (r *reanchorTestReplier) OpenStream(_ context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.openCalls++
	r.lastOptions = opts
	if r.events != nil && r.openEvent != "" {
		r.events.record(r.openEvent)
	}
	return r.stream, nil
}

func (r *reanchorTestReplier) CurrentTaskCardID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cardID
}

func (r *reanchorTestReplier) BindTaskCard(cardID string) {
	if r.onBind != nil {
		r.onBind(cardID)
	}
	r.mu.Lock()
	r.cardID = cardID
	r.mu.Unlock()
	if r.events != nil {
		r.events.record("bind-new-card")
	}
}

func (r *reanchorTestReplier) DeliveryRoute() platform.DeliveryRoute { return r.route }

func (r *reanchorTestReplier) PrepareSupersedeFromReference(_ platform.DurableStreamReference, notice string, operationID string) (platform.SupersedeCheckpoint, error) {
	payload, err := json.Marshal(map[string]string{"notice": notice, "operation_id": operationID})
	return platform.SupersedeCheckpoint{Kind: "reanchor.test.supersede.v1", Payload: payload}, err
}

type reanchorEventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *reanchorEventRecorder) record(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *reanchorEventRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type reanchorTestStream struct {
	mu              sync.Mutex
	updates         []string
	completed       []string
	failed          []string
	superseded      []string
	prepared        []string
	completeStarted chan struct{}
	completeRelease <-chan struct{}
	cardID          string
	presentations   []platform.StreamPresentation
	durableEvent    string
	deliverEvent    string
	events          *reanchorEventRecorder
	beforeDeliver   func()
	deliverStarted  chan struct{}
	deliverRelease  <-chan struct{}
	deliverErr      error
}

func (s *reanchorTestStream) UpdatePresentation(_ context.Context, presentation platform.StreamPresentation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presentations = append(s.presentations, presentation)
	return nil
}

func (s *reanchorTestStream) DurableReference() (platform.DurableStreamReference, error) {
	if s.events != nil && s.durableEvent != "" {
		s.events.record(s.durableEvent)
	}
	payload, err := json.Marshal(map[string]any{"card_id": s.cardID})
	return platform.DurableStreamReference{Kind: "test.stream.v1", Payload: payload}, err
}

func (s *reanchorTestStream) Update(_ context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, content)
	return nil
}

func (s *reanchorTestStream) Complete(_ context.Context, content string) error {
	if s.completeStarted != nil {
		select {
		case s.completeStarted <- struct{}{}:
		default:
		}
	}
	if s.completeRelease != nil {
		<-s.completeRelease
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, content)
	return nil
}

func attachTestTerminalOutbox(t *testing.T, h *Handler) (*terminalOutbox, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	h.terminalOutboxMu.Lock()
	h.terminalOutbox = outbox
	h.terminalOutboxMu.Unlock()
	return outbox, path
}

func newDurableReanchorFixture(t *testing.T) (*Handler, *terminalOutbox, string, *reanchorTestReplier, *reanchorTestReplier, func(string, bool) bool, *progressSession) {
	t.Helper()
	h := NewHandler(nil, nil)
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	outbox, path := attachTestTerminalOutbox(t, h)
	oldReply := newReanchorTestReplier()
	oldReply.route = route
	oldReply.cardID = "card-old"
	oldReply.stream.cardID = "card-old"
	newReply := newReanchorTestReplier()
	newReply.route = route
	newReply.cardID = "card-new"
	newReply.stream.cardID = "card-new"
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), oldReply, "", "codex", "/workspace/project-a", "执行任务", cfg,
	)
	if session.recoveryReservation == "" {
		t.Fatal("initial stream recovery reservation was not created")
	}
	return h, outbox, path, oldReply, newReply, finish, session
}

func TestProgressSessionReanchorPersistsBeforeAuthoritySwitch(t *testing.T) {
	_, outbox, _, oldReply, newReply, _, session := newDurableReanchorFixture(t)
	events := &reanchorEventRecorder{}
	newReply.events, newReply.openEvent = events, "open-new"
	newReply.stream.events, newReply.stream.durableEvent = events, "export-new-reference"
	oldReply.events = events
	oldReply.onBind = func(cardID string) {
		entry := outbox.entryLocked(session.recoveryReservation)
		if entry == nil || entry.Stream == nil || !strings.Contains(string(entry.Stream.Payload), cardID) || len(entry.PendingSupersedes) != 1 {
			t.Fatalf("authority was bound before durable transaction: %#v", entry)
		}
		events.record("persist-new-reference-and-old-supersede")
	}
	oldReply.stream.events, oldReply.stream.deliverEvent = events, "deliver-old-supersede"
	oldReply.stream.beforeDeliver = func() {
		if session.stream != newReply.stream {
			t.Fatalf("old card supersede started before authority swap: stream=%T", session.stream)
		}
		events.record("swap-authority")
	}

	result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
		summary: "进展 A", text: "进展 A", withPrefix: true,
	}, "00000000-0000-4000-8000-000000000201")
	if err != nil || !result.Moved || result.SupersedePending {
		t.Fatalf("reanchor result=%#v err=%v", result, err)
	}
	want := []string{
		"open-new", "export-new-reference", "persist-new-reference-and-old-supersede",
		"bind-new-card", "swap-authority", "deliver-old-supersede",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events=%#v, want %#v", got, want)
	}
	entry := outbox.entryLocked(session.recoveryReservation)
	if entry == nil || len(entry.PendingSupersedes) != 0 {
		t.Fatalf("delivered supersede remained pending: %#v", entry)
	}
}

func TestProgressSessionReanchorPersistenceFailureKeepsOldAuthority(t *testing.T) {
	_, outbox, path, oldReply, newReply, finish, session := newDurableReanchorFixture(t)
	originalPath := outbox.path
	outbox.path = filepath.Join(path, "blocked")
	result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
		summary: "进展 A", text: "进展 A", withPrefix: true,
	}, "00000000-0000-4000-8000-000000000202")
	outbox.path = originalPath
	if err == nil || result.Moved {
		t.Fatalf("reanchor result=%#v err=%v, want persistence failure without move", result, err)
	}
	if session.stream != oldReply.stream || session.reply != oldReply || oldReply.CurrentTaskCardID() != "card-old" {
		t.Fatalf("old authority changed after failed persistence: stream=%T reply=%T card=%q", session.stream, session.reply, oldReply.CurrentTaskCardID())
	}
	if newReply.stream.supersededCount() != 1 || oldReply.stream.supersededCount() != 0 {
		t.Fatalf("cleanup superseded new=%d old=%d", newReply.stream.supersededCount(), oldReply.stream.supersededCount())
	}
	if !session.send("进展 B") || !finish("最终结果", false) {
		t.Fatal("old authority should still accept progress and terminal")
	}
	if oldReply.stream.completedCount() != 1 || newReply.stream.completedCount() != 0 || len(newReply.stream.updateSnapshot()) != 0 {
		t.Fatalf("old completed=%d new completed=%d new updates=%#v", oldReply.stream.completedCount(), newReply.stream.completedCount(), newReply.stream.updateSnapshot())
	}
}

func TestProgressSessionReanchorSupersedeFailureKeepsNewAuthorityAndQueuesRetry(t *testing.T) {
	_, outbox, _, oldReply, newReply, finish, session := newDurableReanchorFixture(t)
	reservationID := session.recoveryReservation
	oldReply.stream.deliverErr = errors.New("temporary CardKit failure")
	result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
		summary: "进展 A", text: "进展 A", withPrefix: true,
	}, "00000000-0000-4000-8000-000000000203")
	if err != nil || !result.Moved || !result.SupersedePending {
		t.Fatalf("reanchor result=%#v err=%v", result, err)
	}
	entry := outbox.entryLocked(reservationID)
	if entry == nil || len(entry.PendingSupersedes) != 1 || entry.PendingSupersedes[0].ID != "00000000-0000-4000-8000-000000000203" {
		t.Fatalf("supersede retry was not retained: %#v", entry)
	}
	if !session.send("进展 B") || !finish("最终结果", false) {
		t.Fatal("new authority should accept progress and terminal")
	}
	if oldReply.stream.completedCount() != 0 || newReply.stream.completedCount() != 1 || len(oldReply.stream.updateSnapshot()) != 0 {
		t.Fatalf("old completed=%d updates=%#v new completed=%d", oldReply.stream.completedCount(), oldReply.stream.updateSnapshot(), newReply.stream.completedCount())
	}
	entry = outbox.entryLocked(reservationID)
	if entry == nil || len(entry.PendingSupersedes) != 1 {
		t.Fatalf("terminal completion discarded pending supersede: %#v", entry)
	}
}

func TestProgressSessionReanchorWinsConcurrentTerminal(t *testing.T) {
	_, _, _, oldReply, newReply, finish, session := newDurableReanchorFixture(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	oldReply.stream.deliverStarted = started
	oldReply.stream.deliverRelease = release
	type moveOutcome struct {
		result progressReanchorResult
		err    error
	}
	moveDone := make(chan moveOutcome, 1)
	go func() {
		result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
			summary: "进展 A", text: "进展 A", withPrefix: true,
		}, "00000000-0000-4000-8000-000000000204")
		moveDone <- moveOutcome{result: result, err: err}
	}()
	<-started
	finishDone := make(chan bool, 1)
	go func() { finishDone <- finish("最终结果", false) }()
	select {
	case <-finishDone:
		t.Fatal("terminal completed while reanchor still held authority lock")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	move := <-moveDone
	if move.err != nil || !move.result.Moved {
		t.Fatalf("reanchor result=%#v err=%v", move.result, move.err)
	}
	if !<-finishDone || oldReply.stream.completedCount() != 0 || newReply.stream.completedCount() != 1 {
		t.Fatalf("terminal did not settle on new authority: old=%d new=%d", oldReply.stream.completedCount(), newReply.stream.completedCount())
	}
}

func TestProgressSessionTerminalWinsConcurrentReanchor(t *testing.T) {
	h := NewHandler(nil, nil)
	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	oldReply.cardID = "card-old"
	newReply.cardID = "card-new"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	oldReply.stream.completeStarted = started
	oldReply.stream.completeRelease = release
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)

	_, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), oldReply, "", "codex", "/workspace/project-a", "执行任务", cfg,
	)
	finishDone := make(chan bool, 1)
	go func() { finishDone <- finish("最终结果", false) }()
	<-started

	type moveResult struct {
		moved bool
		err   error
	}
	moveDone := make(chan moveResult, 1)
	go func() {
		result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
			summary: "进展 A", text: "进展 A", withPrefix: true,
		}, "00000000-0000-4000-8000-000000000301")
		moveDone <- moveResult{moved: result.Moved, err: err}
	}()
	close(release)

	if !<-finishDone {
		t.Fatal("terminal should be consumed by old authoritative stream")
	}
	move := <-moveDone
	if move.err != nil || move.moved || newReply.openCalls != 0 {
		t.Fatalf("terminal must prevent a late reanchor: result=%#v calls=%d", move, newReply.openCalls)
	}
	if oldReply.stream.completedCount() != 1 || oldReply.stream.supersededCount() != 0 {
		t.Fatalf("old completed=%d superseded=%d", oldReply.stream.completedCount(), oldReply.stream.supersededCount())
	}
}

func (s *reanchorTestStream) Fail(_ context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, content)
	return nil
}

func (s *reanchorTestStream) Supersede(_ context.Context, notice string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.superseded = append(s.superseded, notice)
	return nil
}

func (s *reanchorTestStream) DeliverPreparedSupersede(_ context.Context, checkpoint platform.SupersedeCheckpoint) error {
	if s.beforeDeliver != nil {
		s.beforeDeliver()
	}
	if s.events != nil && s.deliverEvent != "" {
		s.events.record(s.deliverEvent)
	}
	if s.deliverStarted != nil {
		select {
		case s.deliverStarted <- struct{}{}:
		default:
		}
	}
	if s.deliverRelease != nil {
		<-s.deliverRelease
	}
	var payload struct {
		Notice string `json:"notice"`
	}
	if err := json.Unmarshal(checkpoint.Payload, &payload); err != nil {
		return err
	}
	s.mu.Lock()
	s.superseded = append(s.superseded, payload.Notice)
	s.mu.Unlock()
	return s.deliverErr
}

func (s *reanchorTestStream) PrepareTerminal(content string, failed bool) (platform.TerminalCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepared = append(s.prepared, content)
	return platform.TerminalCheckpoint{Kind: "reanchor.test.terminal"}, nil
}

func TestProgressSessionReanchorMovesUpdatesAndTerminalToNewStream(t *testing.T) {
	h := NewHandler(nil, nil)
	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	oldReply.cardID = "card-old"
	newReply.cardID = "card-new"
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0

	_, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), oldReply, "", "codex", "/workspace/project-a", "执行任务", cfg,
	)
	if !session.send("进展 A") {
		t.Fatal("initial progress should be sent")
	}

	result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
		summary: "进展 A", text: "进展 A", withPrefix: true,
	}, "00000000-0000-4000-8000-000000000302")
	if err != nil || !result.Moved {
		t.Fatalf("reanchor result=%#v err=%v", result, err)
	}
	if oldReply.stream.supersededCount() != 1 {
		t.Fatalf("old stream superseded=%d, want 1", oldReply.stream.supersededCount())
	}
	if oldReply.CurrentTaskCardID() != "card-new" {
		t.Fatalf("future task interactions still target %q, want card-new", oldReply.CurrentTaskCardID())
	}
	if newReply.openCalls != 1 || !strings.Contains(newReply.lastOptions.InitialContent, "进展 A") ||
		!strings.HasSuffix(newReply.lastOptions.InitialContent, platform.TaskStreamThinkingIndicator) {
		t.Fatalf("new stream calls=%d opts=%#v", newReply.openCalls, newReply.lastOptions)
	}

	if !session.send("进展 B") {
		t.Fatal("progress after reanchor should be sent")
	}
	if got := oldReply.stream.updateSnapshot(); len(got) != 1 ||
		got[0] != "进展 A\n\n"+platform.TaskStreamThinkingIndicator {
		t.Fatalf("old updates=%#v, want only pre-reanchor progress", got)
	}
	if got := newReply.stream.updateSnapshot(); len(got) != 1 ||
		got[0] != "进展 B\n\n"+platform.TaskStreamThinkingIndicator {
		t.Fatalf("new updates=%#v, want post-reanchor progress", got)
	}

	if !finish("最终结果", false) {
		t.Fatal("new stream should consume terminal result")
	}
	if oldReply.stream.completedCount() != 0 || newReply.stream.completedSnapshot()[0] != "最终结果" {
		t.Fatalf("old completed=%#v new completed=%#v", oldReply.stream.completedSnapshot(), newReply.stream.completedSnapshot())
	}
}

func TestProgressSessionReanchorPreservesStructuredTimeline(t *testing.T) {
	h := NewHandler(nil, nil)
	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)

	_, _, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), oldReply, "", "codex", "/workspace/project-a", "执行任务", cfg,
	)
	if !session.send("进展 A") {
		t.Fatal("initial progress should be sent")
	}
	timeline := "**执行进度**\n- ✅ 定位问题\n- • 运行回归测试"
	result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
		summary: timeline, text: timeline, withPrefix: true,
	}, "00000000-0000-4000-8000-000000000303")
	if err != nil || !result.Moved {
		t.Fatalf("reanchor result=%#v err=%v", result, err)
	}
	want := timeline + "\n\n" + platform.TaskStreamThinkingIndicator
	if newReply.lastOptions.InitialContent != want {
		t.Fatalf("initial content=%q, want full structured timeline followed by thinking", newReply.lastOptions.InitialContent)
	}
}

func TestProgressSessionUsesStructuredPresentation(t *testing.T) {
	h := NewHandler(nil, nil)
	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, _, session := h.startProgressSessionForWorkspaceAgentWithHandle(context.Background(), oldReply, "", "codex", "/workspace", "执行任务", cfg)
	timelineItems := []agent.ProgressEvent{
		{ID: "step-1", Kind: agent.ProgressKindCommentary, Sequence: 1, Text: "第一条进度"},
		{ID: "step-2", Kind: agent.ProgressKindCommentary, Sequence: 2, Text: "第二条进度"},
		{ID: "step-3", Kind: agent.ProgressKindCommentary, Sequence: 3, Text: "第三条进度"},
		{ID: "step-4", Kind: agent.ProgressKindCommentary, Sequence: 4, Text: "第四条进度"},
		{ID: "step-5", Kind: agent.ProgressKindCommentary, Sequence: 5, Text: "第五条进度"},
		{ID: "step-6", Kind: agent.ProgressKindCommentary, Sequence: 6, Text: "第六条进度"},
		{ID: "step-7", Kind: agent.ProgressKindCommentary, Sequence: 7, Text: "第七条进度"},
	}
	card, timeline := renderTaskProgressTimeline(timelineItems, "第七条进度")
	if !timeline {
		t.Fatal("expected structured timeline")
	}
	session.onTaskProgress(taskProgressUpdate{
		latest: "最新摘要", card: card, timeline: true, currentExplanation: "继续处理", timelineItems: timelineItems,
	})
	if !session.sendSnapshotContent(progressCardSnapshot{
		summary: "最新摘要", text: card, structured: true, withPrefix: true,
		effectiveProgress: true, timelineItems: timelineItems,
	}) {
		t.Fatal("structured snapshot should be sent")
	}
	oldReply.stream.mu.Lock()
	got := append([]platform.StreamPresentation(nil), oldReply.stream.presentations...)
	oldReply.stream.mu.Unlock()
	if len(got) == 0 || got[len(got)-1].Summary != "最新摘要" || !strings.Contains(got[len(got)-1].Details, "第一条进度") {
		t.Fatalf("presentations=%#v", got)
	}
	if result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
		summary: "忽略此字符串", text: "忽略此字符串", withPrefix: true,
	}, "00000000-0000-4000-8000-000000000304"); err != nil || !result.Moved {
		t.Fatalf("reanchor result=%#v err=%v", result, err)
	}
	newReply.stream.mu.Lock()
	defer newReply.stream.mu.Unlock()
	presentation := newReply.lastOptions.InitialPresentation
	if presentation == nil || presentation.Summary != "最新摘要" {
		t.Fatalf("initial presentation=%#v", newReply.lastOptions.InitialPresentation)
	}
	for _, want := range []string{"第一条进度", "第二条进度", "第七条进度"} {
		if !strings.Contains(presentation.Details, want) {
			t.Fatalf("initial details=%q, want %q", presentation.Details, want)
		}
	}
	for _, hidden := range []string{"第一条进度", "第二条进度"} {
		if strings.Contains(presentation.Preview, hidden) {
			t.Fatalf("initial preview=%q, must omit %q", presentation.Preview, hidden)
		}
	}
	for _, want := range []string{"第三条进度", "第四条进度", "第五条进度", "第六条进度", "第七条进度"} {
		if !strings.Contains(presentation.Preview, want) {
			t.Fatalf("initial preview=%q, want %q", presentation.Preview, want)
		}
	}
}

func TestProgressReanchorConsumesLatestReducerSnapshot(t *testing.T) {
	h := NewHandler(nil, nil)
	oldReply, newReply := newReanchorTestReplier(), newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode, cfg.SendAcceptance = progressModeStream, boolPtr(false)
	_, _, session := h.startProgressSessionForWorkspaceAgentWithHandle(context.Background(), oldReply, "", "codex", "/workspace", "执行任务", cfg)
	task := &activeAgentTask{progress: session}
	for i, event := range []agent.ProgressEvent{{Kind: agent.ProgressKindTool, Sequence: 1, Text: "旧工具步骤"}} {
		if _, ok := task.recordProgressUpdate(time.Now().Add(time.Duration(i)*time.Second), event); !ok {
			t.Fatal("record old progress")
		}
	}
	old := task.view
	oldUpdate := taskProgressUpdate{latest: old.lastProgress, card: "**执行进度**\n- • 旧工具步骤", timeline: true, timelineItems: old.progressTimeline}
	session.onTaskProgress(oldUpdate)
	if !session.sendSnapshotContent(session.latestTaskSnapshot) {
		t.Fatal("send old snapshot")
	}
	if _, ok := task.recordLocalProgressText(time.Now(), "已接收新的补充输入。"); !ok {
		t.Fatal("record local guide")
	}
	_, snapshot, ok := task.progressReanchorSnapshot()
	if !ok {
		t.Fatal("snapshot unavailable")
	}
	if result, err := session.reanchor(context.Background(), newReply, snapshot, "00000000-0000-4000-8000-000000000305"); err != nil || !result.Moved {
		t.Fatalf("reanchor result=%#v err=%v", result, err)
	}
	p := newReply.lastOptions.InitialPresentation
	if p == nil || p.Summary != "已接收新的补充输入。" || !strings.Contains(p.Details, "旧工具步骤") || !strings.Contains(p.Details, "已接收新的补充输入。") {
		t.Fatalf("initial presentation=%#v", p)
	}
}

func TestProgressSessionDurableTerminalUsesReanchoredStream(t *testing.T) {
	h := NewHandler(nil, nil)
	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)

	_, _, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), oldReply, "", "codex", "/workspace/project-a", "执行任务", cfg,
	)
	if result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
		summary: "最新进展", text: "最新进展", withPrefix: true,
	}, "00000000-0000-4000-8000-000000000306"); err != nil || !result.Moved {
		t.Fatalf("reanchor result=%#v err=%v", result, err)
	}
	prepared, err := session.prepareDurableTerminal(newReply, "最终结果", false, false)
	if err != nil || prepared.checkpoint == nil || prepared.checkpoint.Kind != "reanchor.test.terminal" {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	if oldReply.stream.preparedCount() != 0 || newReply.stream.preparedCount() != 1 {
		t.Fatalf("old prepared=%d new prepared=%d", oldReply.stream.preparedCount(), newReply.stream.preparedCount())
	}
}

func TestProgressSessionReanchorPersistsLatestRecoveryCard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	recoveryReply := newOutboxTestReplier(route)
	registry := newOutboxTestRegistry(route, recoveryReply)
	h := NewHandler(nil, nil)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	if err := h.StartTerminalOutbox(workerCtx, registry, path); err != nil {
		t.Fatalf("StartTerminalOutbox: %v", err)
	}
	oldReply := newReanchorTestReplier()
	oldReply.route = route
	oldReply.stream.cardID = "card-old"
	newReply := newReanchorTestReplier()
	newReply.route = route
	newReply.stream.cardID = "card-new"
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, _, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), oldReply, "", "codex", "/workspace/project-a", "执行任务", cfg,
	)
	if result, err := session.reanchor(context.Background(), newReply, progressCardSnapshot{
		summary: "最新进展", text: "最新进展", withPrefix: true,
	}, "00000000-0000-4000-8000-000000000307"); err != nil || !result.Moved {
		t.Fatalf("reanchor result=%#v err=%v", result, err)
	}
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
		t.Fatalf("recover reanchored card: %v", err)
	}
	recoveryReply.mu.Lock()
	defer recoveryReply.mu.Unlock()
	if len(recoveryReply.recoveredReferences) != 1 || !strings.Contains(string(recoveryReply.recoveredReferences[0].Payload), "card-new") {
		t.Fatalf("recovered references=%#v, want latest reanchored card", recoveryReply.recoveredReferences)
	}
}

func TestCodexReturnToRunningTaskReanchorsOnce(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetCodexLocalSessionDir(t.TempDir())
	workspaceA, workspaceB := "/workspace/project-a", "/workspace/project-b"
	threadA, threadB := "thread-a", "thread-b"
	bindingKey := codexBindingKey("route-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, workspaceA, threadA)
	h.ensureCodexSessions().setThread(bindingKey, workspaceB, threadB)
	conversationA := buildCodexConversationID("route-1", "codex", workspaceA)
	conversationB := buildCodexConversationID("route-1", "codex", workspaceB)
	h.commitCodexTaskCardFocus(bindingKey, conversationB)
	// 工作空间卡会在用户选择具体会话前预先更新持久化 workspace；任务卡
	// 重锚必须以最后一次完整接管的会话为准，不能被这一步导航状态吞掉。
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceA)

	state := agent.CodexThreadState{
		ThreadID: threadA, Active: true, ActiveTurnID: "turn-a", Preview: "项目 A 任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	route := codexConversationRoute{
		bindingKey: bindingKey, workspaceRoot: workspaceA,
		conversationID: conversationA, threadID: threadA,
	}
	trace := observability.TraceContext{ConversationID: route.conversationID}
	task, taskCtx, started := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "codex", message: "项目 A 任务",
		codexThreadID: threadA, inProcessCodexLifecycle: true, trace: trace,
	})
	if !started {
		t.Fatal("active task should start")
	}
	defer h.finishActiveTask(route.conversationID, task)

	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, finish, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		taskCtx, oldReply, "", "codex", workspaceA, "项目 A 任务", cfg,
	)
	defer finish("", false)
	task.attachProgressSession(progress)
	task.recordProgressText(testNow(), "项目 A 最新进展")

	req := codexSessionAcquireRequest{
		ctx: context.Background(), taskContext: context.Background(),
		actorUserID: "user-1", authorizedIdentity: "user-1", routeUserID: "route-1", agentName: "codex", agent: ag,
		route: route, platform: platform.PlatformFeishu, reply: newReply,
	}
	result, err := h.acquireCodexSessionWithBindingLocked(req)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !result.progressReanchored || newReply.openCalls != 1 || oldReply.stream.supersededCount() != 1 {
		t.Fatalf("result=%#v new calls=%d old superseded=%d", result, newReply.openCalls, oldReply.stream.supersededCount())
	}
	if !strings.Contains(newReply.lastOptions.InitialContent, "项目 A 最新进展") {
		t.Fatalf("new card initial content=%q", newReply.lastOptions.InitialContent)
	}
	if rendered := h.renderCodexSessionAcquireSuccess(result); !strings.Contains(rendered, "已移到当前消息底部继续更新") {
		t.Fatalf("switch result must explain the new card position: %q", rendered)
	}

	result, err = h.acquireCodexSessionWithBindingLocked(req)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if result.progressReanchored || newReply.openCalls != 1 {
		t.Fatalf("same binding must not reanchor again: result=%#v calls=%d", result, newReply.openCalls)
	}
}

func (s *reanchorTestStream) updateSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.updates...)
}

func (s *reanchorTestStream) completedSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.completed...)
}

func (s *reanchorTestStream) supersededCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.superseded)
}

func (s *reanchorTestStream) completedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.completed)
}

func (s *reanchorTestStream) preparedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prepared)
}

func testNow() (now time.Time) {
	return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
}
