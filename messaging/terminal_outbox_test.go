package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type outboxTestReplier struct {
	mu                    sync.Mutex
	route                 platform.DeliveryRoute
	accepted              map[string]string
	textKeys              []string
	resultKeys            []string
	results               []platform.TerminalResult
	failTextAfterAccept   int
	failResultAfterAccept int
	checkpointCalls       int
	failCheckpoint        int
	failDetachPrepare     int
	detachPrepareCalls    int
	detachPrepareStarted  chan struct{}
	detachPrepareContinue chan struct{}
	checkpointPayloadSeen []json.RawMessage
	supersedeCalls        int
	failSupersede         int
	supersedePayloadSeen  []json.RawMessage
	deliveryOrder         []string
	recoveredReferences   []platform.DurableStreamReference
	recoveredStates       []platform.StreamTerminalState
	stream                *outboxTestStream
	finalOutside          bool
	beforeCheckpoint      func()
	textDelivered         chan string
}

type outboxTextOnlyReplier struct {
	*platformtest.Replier
	keys  []string
	texts []string
}

func (r *outboxTextOnlyReplier) SendTextIdempotent(_ context.Context, text string, key string) error {
	r.keys = append(r.keys, key)
	r.texts = append(r.texts, text)
	return nil
}

func newOutboxTestReplier(route platform.DeliveryRoute) *outboxTestReplier {
	return &outboxTestReplier{route: route, accepted: make(map[string]string)}
}

func (r *outboxTestReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		Text: true, Streaming: true,
		FinalReplyOutsideStream: r.finalOutside, StreamCompletionNotification: !r.finalOutside,
	}
}
func (r *outboxTestReplier) SendText(context.Context, string) error  { return nil }
func (r *outboxTestReplier) SendImage(context.Context, string) error { return nil }
func (r *outboxTestReplier) SendFile(context.Context, string) error  { return nil }
func (r *outboxTestReplier) Typing(context.Context, bool) error      { return nil }
func (r *outboxTestReplier) OpenStream(context.Context, platform.StreamOptions) (platform.Stream, error) {
	if r.stream != nil {
		return r.stream, nil
	}
	return nil, platform.ErrUnsupported
}
func (r *outboxTestReplier) AskChoices(context.Context, string, []platform.Choice) error {
	return platform.ErrUnsupported
}
func (r *outboxTestReplier) DeliveryRoute() platform.DeliveryRoute {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.route
}
func (r *outboxTestReplier) SendTextIdempotent(_ context.Context, text string, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.textKeys = append(r.textKeys, key)
	if _, exists := r.accepted[key]; exists {
		return nil
	}
	r.accepted[key] = text
	if r.failTextAfterAccept > 0 {
		r.failTextAfterAccept--
		return errors.New("ambiguous text response")
	}
	if r.textDelivered != nil {
		select {
		case r.textDelivered <- text:
		default:
		}
	}
	return nil
}
func (r *outboxTestReplier) SendResultIdempotent(_ context.Context, result platform.TerminalResult, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resultKeys = append(r.resultKeys, key)
	if _, exists := r.accepted[key]; exists {
		return nil
	}
	r.accepted[key] = result.Text
	r.results = append(r.results, result)
	if r.failResultAfterAccept > 0 {
		r.failResultAfterAccept--
		return errors.New("ambiguous result response")
	}
	if r.textDelivered != nil {
		select {
		case r.textDelivered <- result.Text:
		default:
		}
	}
	return nil
}
func (r *outboxTestReplier) DeliverTerminal(_ context.Context, checkpoint platform.TerminalCheckpoint) error {
	if r.beforeCheckpoint != nil {
		r.beforeCheckpoint()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpointCalls++
	r.deliveryOrder = append(r.deliveryOrder, "terminal")
	r.checkpointPayloadSeen = append(r.checkpointPayloadSeen, append(json.RawMessage(nil), checkpoint.Payload...))
	if r.failCheckpoint > 0 {
		r.failCheckpoint--
		return errors.New("checkpoint unavailable")
	}
	return nil
}
func (r *outboxTestReplier) DeliverSupersede(_ context.Context, checkpoint platform.SupersedeCheckpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.supersedeCalls++
	r.deliveryOrder = append(r.deliveryOrder, "supersede")
	r.supersedePayloadSeen = append(r.supersedePayloadSeen, append(json.RawMessage(nil), checkpoint.Payload...))
	if r.failSupersede > 0 {
		r.failSupersede--
		return errors.New("supersede unavailable")
	}
	return nil
}
func (r *outboxTestReplier) PrepareTerminalFromReference(reference platform.DurableStreamReference, content string, failed bool) (platform.TerminalCheckpoint, error) {
	state := platform.StreamTerminalCompleted
	if failed {
		state = platform.StreamTerminalFailed
	}
	return r.PrepareTerminalFromReferenceWithState(reference, content, state)
}
func (r *outboxTestReplier) PrepareTerminalFromReferenceWithState(reference platform.DurableStreamReference, content string, state platform.StreamTerminalState) (platform.TerminalCheckpoint, error) {
	r.mu.Lock()
	r.recoveredReferences = append(r.recoveredReferences, reference)
	r.recoveredStates = append(r.recoveredStates, state)
	r.deliveryOrder = append(r.deliveryOrder, "prepare_terminal")
	r.mu.Unlock()
	payload, err := json.Marshal(map[string]any{
		"reference": reference,
		"content":   content,
		"state":     state,
	})
	return platform.TerminalCheckpoint{Kind: "test.recovered-terminal.v1", Payload: payload}, err
}

func (r *outboxTestReplier) PrepareDetachFromReference(reference platform.DurableStreamReference, notice string, operationID string) (platform.SupersedeCheckpoint, error) {
	r.mu.Lock()
	r.detachPrepareCalls++
	call := r.detachPrepareCalls
	started := r.detachPrepareStarted
	continueCh := r.detachPrepareContinue
	if r.failDetachPrepare > 0 {
		r.failDetachPrepare--
		r.mu.Unlock()
		return platform.SupersedeCheckpoint{}, errors.New("detach preparation unavailable")
	}
	r.mu.Unlock()
	if call == 1 && started != nil {
		close(started)
	}
	if call == 1 && continueCh != nil {
		<-continueCh
	}
	payload, err := json.Marshal(map[string]any{
		"reference": reference,
		"notice":    notice,
		"operation": operationID,
		"status":    "detached",
	})
	return platform.SupersedeCheckpoint{Kind: "test.detached-stream.v1", Payload: payload}, err
}

func TestReleasedRecoveryPrepareFailureThenRebindMigratesPreparingHold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message-release",
	}
	reply := newOutboxTestReplier(route)
	reply.failDetachPrepare = 1
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	routeUserID := "feishu:bot:dm:chat:user"
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-rebind"}`)}
	entry, err := outbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stopped: true, Stream: &reference,
		Text: activeStreamRestartText, ActiveStreamRecovery: true,
		Trace: observability.TraceContext{
			TraceID: "trace-rebind", ConversationID: conversationID, ThreadID: "thread-shared", TurnID: "turn-active",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	release := codexReleasedFollowerSnapshot{
		AgentName: "codex", ConversationID: conversationID, ThreadID: "thread-shared", Committed: true,
	}
	h := NewHandler(nil, nil)
	h.recoverReleasedCodexFollowerStreamsForTargets(outbox, []codexReleasedFollowerSnapshot{release})
	follower := codexFollowerSnapshot{
		BindingKey: codexBindingKey(routeUserID, "codex"), RouteUserID: routeUserID,
		AgentName: "codex", ConversationID: conversationID, Revision: 2,
		Target: codexFrontendFollower{
			WorkspaceRoot: workspace, ThreadID: "thread-shared", ActorUserID: "user", DeliveryRoute: route,
		},
	}
	outbox.reconcileCodexFollowerHolds([]codexFollowerSnapshot{follower}, nil)
	if held := outbox.heldCodexFollowerRecoveries(follower); len(held) != 1 || held[0].ID != entry.ID {
		t.Fatalf("rebound follower did not adopt released hold: %#v", held)
	}
}

func TestConcurrentReleasedRecoveryQueuesSingleDetachOperation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message-release",
	}
	reply := newOutboxTestReplier(route)
	reply.detachPrepareStarted = make(chan struct{})
	reply.detachPrepareContinue = make(chan struct{})
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	routeUserID := "feishu:bot:dm:chat:user"
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-concurrent"}`)}
	entry, err := outbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stopped: true, Stream: &reference,
		Text: activeStreamRestartText, ActiveStreamRecovery: true,
		Trace: observability.TraceContext{
			TraceID: "trace-concurrent", ConversationID: conversationID, ThreadID: "thread-shared", TurnID: "turn-active",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	targets := []codexReleasedFollowerSnapshot{{
		AgentName: "codex", ConversationID: conversationID, ThreadID: "thread-shared", Committed: true,
	}}
	firstDone := make(chan struct{})
	go func() {
		h.recoverReleasedCodexFollowerStreamsForTargets(outbox, targets)
		close(firstDone)
	}()
	select {
	case <-reply.detachPrepareStarted:
	case <-time.After(time.Second):
		t.Fatal("first detach preparation did not start")
	}
	secondDone := make(chan struct{})
	go func() {
		h.recoverReleasedCodexFollowerStreamsForTargets(outbox, targets)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("concurrent recovery did not finish")
	}
	close(reply.detachPrepareContinue)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first recovery did not finish")
	}
	loaded, err := loadTerminalOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != entry.ID {
		t.Fatalf("loaded=%#v", loaded)
	}
	if got := len(loaded[0].PendingSupersedes); got != 1 {
		t.Fatalf("detach operations=%d, want 1: %#v", got, loaded[0].PendingSupersedes)
	}
	reply.mu.Lock()
	prepareCalls := reply.detachPrepareCalls
	reply.mu.Unlock()
	if prepareCalls != 1 {
		t.Fatalf("detach prepare calls=%d, want 1", prepareCalls)
	}
}

func (r *outboxTestReplier) PrepareSupersedeFromReference(reference platform.DurableStreamReference, notice string, operationID string) (platform.SupersedeCheckpoint, error) {
	payload, err := json.Marshal(map[string]any{
		"reference": reference,
		"notice":    notice,
		"operation": operationID,
		"status":    "superseded",
	})
	return platform.SupersedeCheckpoint{Kind: "test.superseded-stream.v1", Payload: payload}, err
}

type outboxTestStream struct {
	mu            sync.Mutex
	prepared      int
	updates       []string
	referenceNote string
	referenceHook func()
	beforePrepare func()
	prepareErr    error
	terminalState platform.StreamTerminalState
}

func (s *outboxTestStream) DurableReference() (platform.DurableStreamReference, error) {
	s.mu.Lock()
	content := ""
	if len(s.updates) > 0 {
		content = s.updates[len(s.updates)-1]
	}
	note := s.referenceNote
	s.mu.Unlock()
	payload, err := json.Marshal(map[string]any{"card_id": "card-1", "sequence": 7, "content": content, "note": note})
	if err != nil {
		return platform.DurableStreamReference{}, err
	}
	return platform.DurableStreamReference{
		Kind:    "test.stream.v1",
		Payload: payload,
	}, nil
}

func (s *outboxTestStream) SetDurableReferenceChangeHandler(handler func()) {
	s.mu.Lock()
	s.referenceHook = handler
	s.mu.Unlock()
}

func (s *outboxTestStream) changeDurableReference(note string) {
	s.mu.Lock()
	s.referenceNote = note
	handler := s.referenceHook
	s.mu.Unlock()
	if handler != nil {
		handler()
	}
}

func (s *outboxTestStream) Update(_ context.Context, content string) error {
	s.mu.Lock()
	s.updates = append(s.updates, content)
	s.mu.Unlock()
	return nil
}
func (s *outboxTestStream) Complete(context.Context, string) error {
	return errors.New("legacy Complete must not run")
}
func (s *outboxTestStream) Fail(context.Context, string) error {
	return errors.New("legacy Fail must not run")
}
func (s *outboxTestStream) Stop(_ context.Context, _ string) error {
	s.mu.Lock()
	s.terminalState = platform.StreamTerminalStopped
	s.mu.Unlock()
	return nil
}
func (s *outboxTestStream) PrepareTerminal(content string, failed bool) (platform.TerminalCheckpoint, error) {
	state := platform.StreamTerminalCompleted
	if failed {
		state = platform.StreamTerminalFailed
	}
	return s.prepareTerminalWithState(content, state)
}
func (s *outboxTestStream) PrepareTerminalWithState(content string, state platform.StreamTerminalState) (platform.TerminalCheckpoint, error) {
	return s.prepareTerminalWithState(content, state)
}
func (s *outboxTestStream) prepareTerminalWithState(content string, state platform.StreamTerminalState) (platform.TerminalCheckpoint, error) {
	if s.beforePrepare != nil {
		s.beforePrepare()
	}
	s.mu.Lock()
	s.prepared++
	s.terminalState = state
	s.mu.Unlock()
	if s.prepareErr != nil {
		return platform.TerminalCheckpoint{}, s.prepareErr
	}
	payload, err := json.Marshal(map[string]any{
		"content": content,
		"failed":  state == platform.StreamTerminalFailed,
		"state":   state,
	})
	return platform.TerminalCheckpoint{Kind: "test.terminal.v1", Payload: payload}, err
}

func TestPrepareDurableTerminalCarriesLatestProgressButKeepsFinalAnswerOutsideCard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reply := newOutboxTestReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, ChatID: "chat-1"})
	reply.finalOutside = true
	stream := &outboxTestStream{}
	reply.stream = stream
	session := &progressSession{
		ctx: ctx, cancel: cancel, reply: reply, stream: stream,
		cfg:        config.ProgressConfig{Mode: progressModeStream, InitialDelaySeconds: 1},
		snapshotCh: make(chan progressCardSnapshot, 1),
	}
	card := "**执行进度**\n- ✅ 检查项目\n- • 运行测试\n\n**当前说明**\n正在完成最后验证。"
	session.onTaskProgress(taskProgressUpdate{
		card: card, timeline: true, currentExplanation: "正在完成最后验证。",
		timelineItems: []agent.ProgressEvent{
			{ID: "plan:1", Kind: agent.ProgressKindPlan, State: agent.ProgressStateCompleted, Text: "检查项目"},
			{ID: "command:1", Kind: agent.ProgressKindCommand, State: agent.ProgressStateRunning, Text: "运行测试"},
		},
	})

	prepared, err := session.prepareDurableTerminal(reply, "最终结果", false, false)
	if err != nil {
		t.Fatalf("prepareDurableTerminal: %v", err)
	}
	if prepared.checkpoint == nil {
		t.Fatal("durable terminal checkpoint is nil")
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(prepared.checkpoint.Payload, &payload); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	if payload.Content != card || prepared.consumed {
		t.Fatalf("content=%q consumed=%v, want latest progress without final answer", payload.Content, prepared.consumed)
	}
}

type outboxTestPlatform struct {
	name    platform.PlatformName
	account string
	reply   *outboxTestReplier
}

func (p *outboxTestPlatform) Name() platform.PlatformName         { return p.name }
func (p *outboxTestPlatform) AccountID() string                   { return p.account }
func (p *outboxTestPlatform) Capabilities() platform.Capabilities { return p.reply.Capabilities() }
func (p *outboxTestPlatform) Run(ctx context.Context, _ platform.DispatchFunc) error {
	<-ctx.Done()
	return nil
}
func (p *outboxTestPlatform) NewReplier(chatID string) platform.Replier {
	return p.NewReplierForRoute(platform.DeliveryRoute{Platform: p.name, AccountID: p.account, ChatID: chatID})
}
func (p *outboxTestPlatform) NewReplierForRoute(route platform.DeliveryRoute) platform.Replier {
	p.reply.mu.Lock()
	p.reply.route = route
	p.reply.mu.Unlock()
	return p.reply
}

func newOutboxTestRegistry(route platform.DeliveryRoute, reply *outboxTestReplier) *platform.Registry {
	return platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &outboxTestPlatform{name: route.Platform, account: route.AccountID, reply: reply},
		Access:   platform.NewAccessControl([]string{"test-user"}),
	}})
}

func TestTerminalOutboxConvertsReleasedCodexFollowerRecoveryBeforeWorkerStarts(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	workspace := t.TempDir()
	routeUserID := "feishu:bot:dm:chat:user"
	bindingKey := codexBindingKey(routeUserID, "codex")
	first := NewHandler(nil, nil)
	first.SetCodexSessionFile(statePath)
	first.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	first.ensureCodexSessions().setThread(bindingKey, workspace, "thread-released")
	if _, err := first.ensureCodexSessions().releaseWorkspaceThread(bindingKey, workspace); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message",
	}
	oldOutbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-released"}`)}
	trace := observability.TraceContext{
		TraceID: "trace-release", ConversationID: buildCodexConversationID(routeUserID, "codex", workspace),
		ThreadID: "thread-released", TurnID: "turn-active",
	}
	entry, err := oldOutbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。", Trace: trace,
	})
	if err != nil {
		t.Fatal(err)
	}

	restarted := NewHandler(nil, nil)
	restarted.SetCodexSessionFile(statePath)
	reply := newOutboxTestReplier(route)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restarted.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, reply), path); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTerminalOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != entry.ID {
		t.Fatalf("loaded=%#v", loaded)
	}
	recovered := loaded[0]
	if recovered.Stream != nil || recovered.Checkpoint != nil || recovered.Text != "" || !recovered.TextDelivered {
		t.Fatalf("released recovery still has terminal payload: %#v", recovered)
	}
	if len(recovered.PendingSupersedes) != 1 || recovered.PendingSupersedes[0].Checkpoint.Kind != "test.detached-stream.v1" {
		t.Fatalf("released recovery detach=%#v", recovered.PendingSupersedes)
	}
	if due := restarted.currentTerminalOutbox().dueIDs(); len(due) != 0 {
		t.Fatalf("released recovery remains terminal-deliverable: %v", due)
	}
}

func TestReleasedCodexFollowerRecoveryRetriesWithoutProcessRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	workspace := t.TempDir()
	routeUserID := "feishu:bot:dm:chat:user"
	bindingKey := codexBindingKey(routeUserID, "codex")
	first := NewHandler(nil, nil)
	first.SetCodexSessionFile(statePath)
	first.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	first.ensureCodexSessions().setThread(bindingKey, workspace, "thread-released")
	if _, err := first.ensureCodexSessions().releaseWorkspaceThread(bindingKey, workspace); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message",
	}
	oldOutbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-released"}`)}
	trace := observability.TraceContext{
		TraceID: "trace-release", ConversationID: buildCodexConversationID(routeUserID, "codex", workspace),
		ThreadID: "thread-released", TurnID: "turn-active",
	}
	entry, err := oldOutbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。", Trace: trace,
	})
	if err != nil {
		t.Fatal(err)
	}

	restarted := NewHandler(nil, nil)
	restarted.SetCodexSessionFile(statePath)
	reply := newOutboxTestReplier(route)
	reply.failDetachPrepare = 1
	registry := newOutboxTestRegistry(route, reply)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restarted.StartTerminalOutbox(ctx, registry, path); err != nil {
		t.Fatal(err)
	}
	if due := restarted.currentTerminalOutbox().dueIDs(); len(due) != 0 {
		t.Fatalf("failed first detach exposed terminal recovery: %v", due)
	}
	restarted.reconcileCodexFollowers(context.Background(), registry)
	loaded, err := loadTerminalOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != entry.ID || loaded[0].Stream != nil || loaded[0].Text != "" || len(loaded[0].PendingSupersedes) != 1 {
		t.Fatalf("released recovery was not retried in-process: %#v", loaded)
	}
	if due := restarted.currentTerminalOutbox().dueIDs(); len(due) != 0 {
		t.Fatalf("retried release remains terminal-deliverable: %v", due)
	}
}

func TestReleasedFollowerRecoveryPreservesPriorTurnStagedTerminal(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	workspace := t.TempDir()
	routeUserID := "feishu:bot:dm:chat:user"
	bindingKey := codexBindingKey(routeUserID, "codex")
	first := NewHandler(nil, nil)
	first.SetCodexSessionFile(statePath)
	first.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	first.ensureCodexSessions().setThread(bindingKey, workspace, "thread-released")
	if _, err := first.ensureCodexSessions().releaseWorkspaceThread(bindingKey, workspace); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message",
	}
	oldOutbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-prior"}`)}
	entry, err := oldOutbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。", ActiveStreamRecovery: true,
		Trace: observability.TraceContext{
			TraceID: "trace-prior", ConversationID: buildCodexConversationID(routeUserID, "codex", workspace),
			ThreadID: "thread-released", TurnID: "turn-prior",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := oldOutbox.stageReservationResult(entry.ID, terminalOutboxDraft{
		Route: route, AgentName: "codex", Text: "上一轮最终结果",
		Trace: observability.TraceContext{
			TraceID: "trace-prior", ConversationID: buildCodexConversationID(routeUserID, "codex", workspace),
			ThreadID: "thread-released", TurnID: "turn-prior",
		},
	}); err != nil {
		t.Fatal(err)
	}

	restarted := NewHandler(nil, nil)
	restarted.SetCodexSessionFile(statePath)
	reply := newOutboxTestReplier(route)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restarted.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, reply), path); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTerminalOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Stream == nil || loaded[0].Text != "上一轮最终结果" ||
		len(loaded[0].PendingSupersedes) != 0 {
		t.Fatalf("prior staged terminal was consumed by release recovery: %#v", loaded)
	}
	if due := restarted.currentTerminalOutbox().dueIDs(); !reflect.DeepEqual(due, []string{entry.ID}) {
		t.Fatalf("prior staged terminal due=%v, want [%s]", due, entry.ID)
	}
}

func TestReleaseFirstTurnPredecessorCrashFreezesExactReservation(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	outboxPath := filepath.Join(t.TempDir(), "terminal-outbox.json")
	workspace := t.TempDir()
	routeUserID := "feishu:bot:dm:chat:user"
	bindingKey := codexBindingKey(routeUserID, "codex")
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message",
	}

	first := NewHandler(nil, nil)
	first.SetCodexSessionFile(statePath)
	first.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	first.ensureCodexSessions().setThread(bindingKey, workspace, "thread-old")
	outbox, err := newTerminalOutbox(outboxPath, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-first-turn"}`)}
	journal, err := outbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。", ActiveStreamRecovery: true,
		Trace: observability.TraceContext{TraceID: "trace-journal", ConversationID: conversationID, ThreadID: "thread-old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := outbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。", ActiveStreamRecovery: true,
		Trace: observability.TraceContext{TraceID: "trace-other", ConversationID: conversationID, ThreadID: "thread-old", TurnID: "turn-other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ensureCodexSessions().replaceRemoteFirstTurnThread(
		bindingKey, workspace, conversationID, "thread-old", "thread-new", journal.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ensureCodexSessions().releaseWorkspaceThread(bindingKey, workspace); err != nil {
		t.Fatal(err)
	}

	restarted := NewHandler(nil, nil)
	restarted.SetCodexSessionFile(statePath)
	reply := newOutboxTestReplier(route)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restarted.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, reply), outboxPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTerminalOutbox(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*terminalOutboxEntry, len(loaded))
	for _, entry := range loaded {
		byID[entry.ID] = entry
	}
	if recovered := byID[journal.ID]; recovered == nil || recovered.Stream != nil || recovered.Text != "" ||
		len(recovered.PendingSupersedes) != 1 {
		t.Fatalf("first-turn release reservation was not frozen: %#v", recovered)
	}
	if untouched := byID[other.ID]; untouched == nil || untouched.Stream == nil || untouched.Text == "" ||
		len(untouched.PendingSupersedes) != 0 {
		t.Fatalf("unrelated predecessor reservation was changed: %#v", untouched)
	}
}

func TestTerminalOutboxHoldsFollowerRecoveryAcrossReplyMessages(t *testing.T) {
	h, _, _, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	close(watchDone)
	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	entryRoute := snapshot.Target.DeliveryRoute
	entryRoute.ReplyToID = "message-before-restart"
	oldOutbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-active"}`)}
	entry, err := oldOutbox.reserve(terminalOutboxDraft{
		Route: entryRoute, AgentName: snapshot.AgentName, Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。", ActiveStreamRecovery: true,
		Trace: observability.TraceContext{
			TraceID: "trace-active", ConversationID: snapshot.ConversationID,
			ThreadID: snapshot.Target.ThreadID, TurnID: "turn-local-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reply := newOutboxTestReplier(snapshot.Target.DeliveryRoute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.StartTerminalOutbox(ctx, newOutboxTestRegistry(snapshot.Target.DeliveryRoute, reply), path); err != nil {
		t.Fatal(err)
	}
	outbox := h.currentTerminalOutbox()
	outbox.mu.Lock()
	held := outbox.followerHeld[entry.ID]
	outbox.mu.Unlock()
	if !held {
		t.Fatal("active recovery was not held after only ReplyToID changed")
	}
	if due := outbox.dueIDs(); len(due) != 0 {
		t.Fatalf("cross-message recovery became terminal-deliverable: %v", due)
	}
}

func TestFollowerBindingRemovalReleasesStaleRecoveryHold(t *testing.T) {
	h, _, registry, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	close(watchDone)
	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	oldOutbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-active"}`)}
	entry, err := oldOutbox.reserve(terminalOutboxDraft{
		Route: snapshot.Target.DeliveryRoute, AgentName: snapshot.AgentName, Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。", ActiveStreamRecovery: true,
		Trace: observability.TraceContext{
			TraceID: "trace-active", ConversationID: snapshot.ConversationID,
			ThreadID: snapshot.Target.ThreadID, TurnID: "turn-local-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.StartTerminalOutbox(ctx, registry, path); err != nil {
		t.Fatal(err)
	}
	store := h.ensureCodexSessions()
	store.mu.Lock()
	binding := store.bindings[snapshot.BindingKey]
	binding.Follower = nil
	binding.FollowRevision++
	store.bindings[snapshot.BindingKey] = binding
	store.mu.Unlock()
	store.save()

	h.reconcileCodexFollowers(context.Background(), registry)
	outbox := h.currentTerminalOutbox()
	outbox.mu.Lock()
	held := outbox.followerHeld[entry.ID]
	outbox.mu.Unlock()
	if held {
		t.Fatal("removed follower left recovery held for the process lifetime")
	}
	if due := outbox.dueIDs(); !reflect.DeepEqual(due, []string{entry.ID}) {
		t.Fatalf("released stale hold due=%v, want [%s]", due, entry.ID)
	}
}

func TestTerminalOutboxHoldsActiveCodexFollowerRecoveryBeforeWorkerStarts(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	h, _, _, snapshot, watchDone := newCodexFollowerFixtureWithStatePath(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	}, statePath)
	close(watchDone)
	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	route := snapshot.Target.DeliveryRoute
	oldOutbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-active"}`)}
	entry, err := oldOutbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: snapshot.AgentName, Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。",
		Trace: observability.TraceContext{
			TraceID: "trace-active", ConversationID: snapshot.ConversationID,
			ThreadID: snapshot.Target.ThreadID, TurnID: "turn-local-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reply := newOutboxTestReplier(route)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, reply), path); err != nil {
		t.Fatal(err)
	}
	outbox := h.currentTerminalOutbox()
	outbox.mu.Lock()
	held := outbox.preparing[entry.ID]
	outbox.mu.Unlock()
	if !held {
		t.Fatal("active follower recovery was not held before worker startup")
	}
	if due := outbox.dueIDs(); len(due) != 0 {
		t.Fatalf("held follower recovery is terminal-deliverable: %v", due)
	}
}

func TestCodexFollowerRecoveryKeepsPriorTurnTerminalWhileNewTurnIsActive(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	h, _, _, snapshot, watchDone := newCodexFollowerFixtureWithStatePath(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-2",
	}, statePath)
	close(watchDone)
	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	route := snapshot.Target.DeliveryRoute
	oldOutbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-prior-turn"}`)}
	entry, err := oldOutbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: snapshot.AgentName, Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。",
		Trace: observability.TraceContext{
			TraceID: "trace-prior-turn", ConversationID: snapshot.ConversationID,
			ThreadID: snapshot.Target.ThreadID, TurnID: "turn-local-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := oldOutbox.stageReservationResult(entry.ID, terminalOutboxDraft{
		Route: route, AgentName: snapshot.AgentName, Text: "上一轮最终结果",
		Trace: observability.TraceContext{
			TraceID: "trace-prior-turn", ConversationID: snapshot.ConversationID,
			ThreadID: snapshot.Target.ThreadID, TurnID: "turn-local-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	reply := newOutboxTestReplier(route)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, reply), path); err != nil {
		t.Fatal(err)
	}
	state := externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
		ThreadID: snapshot.Target.ThreadID, Active: true, ActiveTurnID: "turn-local-2",
	}}
	if err := h.reconcileCodexFollowerRecoveries(snapshot, state, reply); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadTerminalOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != entry.ID || loaded[0].Stream == nil ||
		loaded[0].Text != "上一轮最终结果" || len(loaded[0].PendingSupersedes) != 0 {
		t.Fatalf("prior turn terminal was overwritten by active turn recovery: %#v", loaded)
	}
	if due := h.currentTerminalOutbox().dueIDs(); !reflect.DeepEqual(due, []string{entry.ID}) {
		t.Fatalf("prior turn terminal due=%v, want [%s]", due, entry.ID)
	}
	reply.mu.Lock()
	supersedeCalls := reply.supersedeCalls
	reply.mu.Unlock()
	if supersedeCalls != 0 {
		t.Fatalf("prior turn card was superseded by the new turn: calls=%d", supersedeCalls)
	}
}

func TestCodexFirstTurnRecoveryRepairsOnlyJournalReservation(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	outboxPath := filepath.Join(t.TempDir(), "terminal-outbox.json")
	workspace := filepath.Join(t.TempDir(), "project")
	routeUserID := "feishu:bot:dm:chat:user"
	bindingKey := codexBindingKey(routeUserID, "codex")
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message",
	}

	h := NewHandler(nil, nil)
	h.SetCodexSessionFile(statePath)
	selection := h.ensureCodexSessions().remoteSelectionSnapshot(bindingKey, "thread-old")
	if _, err := h.ensureCodexSessions().commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspace, ConversationID: conversationID,
		TargetThreadID: "thread-old", PendingFirstTurn: true, SetFollower: true,
		Follower: &codexFrontendFollower{
			WorkspaceRoot: workspace, ThreadID: "thread-old", ActorUserID: "user",
			DeliveryRoute: route,
		},
		Expected: selection,
	}); err != nil {
		t.Fatal(err)
	}
	outbox, err := newTerminalOutbox(outboxPath, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-first-turn"}`)}
	journalEntry, err := outbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。",
		Trace: observability.TraceContext{
			TraceID: "trace-first-turn", ConversationID: conversationID,
			ThreadID: "thread-old", TurnID: "",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	priorEntry, err := outbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。",
		Trace: observability.TraceContext{
			TraceID: "trace-prior", ConversationID: conversationID,
			ThreadID: "thread-old", TurnID: "turn-prior",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.stageReservationResult(priorEntry.ID, terminalOutboxDraft{
		Route: route, AgentName: "codex", Text: "上一轮最终结果",
		Trace: observability.TraceContext{
			TraceID: "trace-prior", ConversationID: conversationID,
			ThreadID: "thread-old", TurnID: "turn-prior",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.ensureCodexSessions().replaceRemoteFirstTurnThread(
		bindingKey, workspace, conversationID, "thread-old", "thread-new", journalEntry.ID,
	); err != nil {
		t.Fatal(err)
	}
	snapshots := h.ensureCodexSessions().followerSnapshots()
	if len(snapshots) != 1 {
		t.Fatalf("followers=%#v", snapshots)
	}

	reply := newOutboxTestReplier(route)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, reply), outboxPath); err != nil {
		t.Fatal(err)
	}
	activeOutbox := h.currentTerminalOutbox()
	activeOutbox.mu.Lock()
	journalHeld := activeOutbox.followerHeld[journalEntry.ID]
	priorHeld := activeOutbox.followerHeld[priorEntry.ID]
	activeOutbox.mu.Unlock()
	if !journalHeld || priorHeld {
		t.Fatalf("held journal=%v prior=%v, want true false", journalHeld, priorHeld)
	}
	state := externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
		ThreadID: "thread-new", Active: true, ActiveTurnID: "turn-new",
	}}
	if err := h.reconcileCodexFollowerRecoveries(snapshots[0], state, reply); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadTerminalOutbox(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	var repaired, prior *terminalOutboxEntry
	for _, entry := range loaded {
		switch entry.ID {
		case journalEntry.ID:
			repaired = entry
		case priorEntry.ID:
			prior = entry
		}
	}
	if repaired == nil || repaired.Trace == nil || repaired.Trace.ThreadID != "thread-new" ||
		repaired.Trace.TurnID != "turn-new" || repaired.Stream != nil || repaired.Text != "" ||
		len(repaired.PendingSupersedes) != 1 {
		t.Fatalf("journal reservation was not repaired exactly: %#v", repaired)
	}
	if prior == nil || prior.Trace == nil || prior.Trace.ThreadID != "thread-old" ||
		prior.Trace.TurnID != "turn-prior" || prior.Stream == nil || prior.Text != "上一轮最终结果" ||
		len(prior.PendingSupersedes) != 0 {
		t.Fatalf("prior reservation was changed by first-turn repair: %#v", prior)
	}
	if due := activeOutbox.dueIDs(); !reflect.DeepEqual(due, []string{priorEntry.ID}) {
		t.Fatalf("due=%v, want prior reservation only", due)
	}
}

func TestCodexFollowerRestartFreezesHeldCardAndStartsNewObserver(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	h, ag, _, snapshot, watchDone := newCodexFollowerFixtureWithStatePath(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	}, statePath)
	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	route := snapshot.Target.DeliveryRoute
	oldOutbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-before-restart"}`)}
	_, err = oldOutbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: snapshot.AgentName, Stopped: true, Stream: &reference,
		Text: "任务已中断。WeClaw 服务在任务执行期间发生重启。",
		Trace: observability.TraceContext{
			TraceID: "trace-restart", ConversationID: snapshot.ConversationID,
			ThreadID: snapshot.Target.ThreadID, TurnID: "turn-local-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
	registry := newOutboxTestRegistry(route, reply)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := h.StartTerminalOutbox(ctx, registry, path); err != nil {
		t.Fatal(err)
	}
	if err := h.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ag.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("restored follower observer did not start")
	}
	waitForRolloverCondition(t, func() bool {
		reply.mu.Lock()
		defer reply.mu.Unlock()
		return reply.supersedeCalls == 1
	})
	reply.mu.Lock()
	checkpointCalls := reply.checkpointCalls
	results := len(reply.results)
	textKeys := len(reply.textKeys)
	reply.mu.Unlock()
	if checkpointCalls != 0 || results != 0 || textKeys != 0 {
		t.Fatalf("restart emitted false terminal delivery: checkpoint=%d results=%d text=%d", checkpointCalls, results, textKeys)
	}
	if _, active := h.activeTask(snapshot.ConversationID); !active {
		t.Fatal("restored follower observer is not active")
	}

	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-local", Active: false, LastTurnID: "turn-local-1", LastTurnStatus: "completed",
	})
	close(watchDone)
	waitForRolloverCondition(t, func() bool {
		_, active := h.activeTask(snapshot.ConversationID)
		return !active
	})
	cancel()
	waitForRolloverCondition(t, func() bool {
		h.codexFollowerMu.Lock()
		defer h.codexFollowerMu.Unlock()
		return h.codexFollower == nil
	})
}

func waitForTerminalOutboxEmpty(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, err := loadTerminalOutbox(path)
		if err != nil {
			t.Fatalf("load terminal outbox: %v", err)
		}
		if len(entries) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal outbox still contains %#v", entries)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTerminalOutboxPersistsAtomicallyWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", terminalOutboxFileName)
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatalf("newTerminalOutbox: %v", err)
	}
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	entry, err := outbox.enqueue(terminalOutboxDraft{
		Route: route, AgentName: "codex", ResultTitle: "Codex · jumpserver", RichResult: true, Text: "最终结果",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("entry id is empty")
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat outbox: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("outbox mode=%o, want 600", fileInfo.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat outbox dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("outbox dir mode=%v", dirInfo.Mode().Perm())
	}
	loaded, err := loadTerminalOutbox(path)
	if err != nil || len(loaded) != 1 || loaded[0].Text != "最终结果" ||
		loaded[0].ResultTitle != "Codex · jumpserver" || !loaded[0].RichResult {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestTerminalOutboxDeliversRichTerminalResultInsteadOfPlainText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}

	if err := outbox.enqueueAndAttempt(context.Background(), terminalOutboxDraft{
		Route: route, AgentName: "codex", ResultTitle: "Codex · jumpserver", RichResult: true,
		Text: "### 最终结果", Failed: true,
	}, newSerializedReplier(reply)); err != nil {
		t.Fatal(err)
	}

	reply.mu.Lock()
	defer reply.mu.Unlock()
	if len(reply.textKeys) != 0 || len(reply.resultKeys) != 1 || len(reply.results) != 1 {
		t.Fatalf("text keys=%#v result keys=%#v results=%#v", reply.textKeys, reply.resultKeys, reply.results)
	}
	result := reply.results[0]
	if result.Title != "Codex · jumpserver" || result.Text != "### 最终结果" || result.State != platform.StreamTerminalFailed {
		t.Fatalf("result=%#v", result)
	}
}

func TestSendOutboxResultFallsBackToIdempotentTextWhenRichCapabilityIsMissing(t *testing.T) {
	reply := &outboxTextOnlyReplier{Replier: platformtest.NewReplier(platform.Capabilities{Text: true})}
	entry := &terminalOutboxEntry{ResultTitle: "Codex · jumpserver", RichResult: true}

	if err := sendOutboxResult(context.Background(), reply, entry, "### 最终结果", "delivery-1:result"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reply.keys, []string{"delivery-1:result"}) ||
		!reflect.DeepEqual(reply.texts, []string{"### 最终结果"}) {
		t.Fatalf("keys=%#v texts=%#v", reply.keys, reply.texts)
	}
}

func TestTerminalOutboxRetriesAmbiguousRichResultWithSameKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.failResultAfterAccept = 1
	registry := newOutboxTestRegistry(route, reply)
	first, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := first.enqueue(terminalOutboxDraft{
		Route: route, AgentName: "claude", ResultTitle: "Claude · jumpserver", RichResult: true, Text: "完整回答",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.attempt(context.Background(), entry.ID, reply); err == nil {
		t.Fatal("first attempt must expose ambiguous delivery")
	}
	second, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.attempt(context.Background(), entry.ID, nil); err != nil {
		t.Fatalf("retry: %v", err)
	}

	reply.mu.Lock()
	defer reply.mu.Unlock()
	if len(reply.resultKeys) != 2 || reply.resultKeys[0] != reply.resultKeys[1] || len(reply.results) != 1 {
		t.Fatalf("result keys=%#v results=%#v", reply.resultKeys, reply.results)
	}
	if len(reply.textKeys) != 0 {
		t.Fatalf("ambiguous card delivery must not fall back to text: %#v", reply.textKeys)
	}
}

func TestTerminalOutboxKeepsCommittedEntryWhenDirectorySyncFails(t *testing.T) {
	originalSync := syncTerminalOutboxDirectory
	syncTerminalOutboxDirectory = func(string) error {
		return errors.New("injected directory sync failure")
	}
	t.Cleanup(func() {
		syncTerminalOutboxDirectory = originalSync
	})

	path := filepath.Join(t.TempDir(), "state", terminalOutboxFileName)
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatalf("newTerminalOutbox: %v", err)
	}
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	entry, err := outbox.enqueue(terminalOutboxDraft{Route: route, AgentName: "codex", Text: "最终结果"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(outbox.entries) != 1 || outbox.entries[0].ID != entry.ID {
		t.Fatalf("committed in-memory entries=%#v", outbox.entries)
	}
	loaded, err := loadTerminalOutbox(path)
	if err != nil {
		t.Fatalf("loadTerminalOutbox: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != entry.ID {
		t.Fatalf("committed disk entries=%#v", loaded)
	}
}

func TestTerminalOutboxStatusIsBoundedAndOmitsPayloadAndRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return now }
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "secret-account", ChatID: "secret-chat",
	}
	entry, err := outbox.enqueue(terminalOutboxDraft{
		Route: route, AgentName: "codex", Text: "secret-terminal-payload",
	})
	if err != nil {
		t.Fatal(err)
	}
	outbox.mu.Lock()
	entry = outbox.entryLocked(entry.ID)
	entry.Attempts = 3
	entry.LastError = "temporary failure"
	entry.UpdatedAt = now.Add(time.Minute)
	entry.NextAttempt = now.Add(2 * time.Minute)
	if err := outbox.persistLocked(); err != nil {
		outbox.mu.Unlock()
		t.Fatal(err)
	}
	outbox.mu.Unlock()

	status := outbox.status()
	if status.Pending != 1 || len(status.Entries) != 1 || status.Entries[0].Attempts != 3 {
		t.Fatalf("status=%#v", status)
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-account", "secret-chat", "secret-terminal-payload"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, data)
		}
	}
}

func TestTerminalOutboxRedrivePersistsScheduleAndPreservesAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	entry, err := outbox.enqueue(terminalOutboxDraft{Route: route, Text: "结果"})
	if err != nil {
		t.Fatal(err)
	}
	outbox.mu.Lock()
	stored := outbox.entryLocked(entry.ID)
	stored.Attempts = 4
	stored.NextAttempt = base.Add(time.Hour)
	stored.UpdatedAt = base
	if err := outbox.persistLocked(); err != nil {
		outbox.mu.Unlock()
		t.Fatal(err)
	}
	outbox.mu.Unlock()
	redriveAt := base.Add(5 * time.Minute)
	outbox.now = func() time.Time { return redriveAt }

	result, err := outbox.redrive(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Requested != 1 || result.Status.Entries[0].Attempts != 4 {
		t.Fatalf("result=%#v", result)
	}
	loaded, err := loadTerminalOutbox(path)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	if !loaded[0].NextAttempt.Equal(redriveAt) || loaded[0].Attempts != 4 {
		t.Fatalf("entry=%#v", loaded[0])
	}
	if _, err := outbox.redrive("00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrTerminalOutboxNotFound) {
		t.Fatalf("missing id error=%v", err)
	}
}

func TestTerminalOutboxMovesPermanentFailureToDeadLetterAndRedrives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	outbox.maxAttempts = 2
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "removed", ChatID: "oc_chat"}
	entry, err := outbox.enqueue(terminalOutboxDraft{Route: route, Text: "结果"})
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < outbox.maxAttempts; attempt++ {
		if err := outbox.attempt(context.Background(), entry.ID, nil); err == nil {
			t.Fatal("attempt unexpectedly succeeded")
		}
	}
	status := outbox.status()
	if status.Pending != 0 || status.DeadLetter != 1 || len(status.Entries) != 1 ||
		!status.Entries[0].DeadLetter || status.Entries[0].Attempts != outbox.maxAttempts {
		t.Fatalf("dead-letter status=%#v", status)
	}
	if due := outbox.dueIDs(); len(due) != 0 {
		t.Fatalf("dead letter remained due: %#v", due)
	}

	result, err := outbox.redrive(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Requested != 1 || result.Status.Pending != 1 || result.Status.DeadLetter != 0 ||
		result.Status.Entries[0].Attempts != outbox.maxAttempts {
		t.Fatalf("redrive result=%#v", result)
	}
	if due := outbox.dueIDs(); len(due) != 1 || due[0] != entry.ID {
		t.Fatalf("redriven due ids=%#v", due)
	}
}

func TestTerminalOutboxEvictsDeadLetterToReserveNewDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	outbox.maxEntries = 2
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	old, err := outbox.enqueue(terminalOutboxDraft{Route: route, Text: "旧死信"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.enqueue(terminalOutboxDraft{Route: route, Text: "仍待投递"}); err != nil {
		t.Fatal(err)
	}
	outbox.mu.Lock()
	stored := outbox.entryLocked(old.ID)
	stored.DeadLetter = true
	stored.DeadLetterAt = base
	if err := outbox.persistLocked(); err != nil {
		outbox.mu.Unlock()
		t.Fatal(err)
	}
	outbox.mu.Unlock()

	fresh, err := outbox.reserve(terminalOutboxDraft{Route: route, Text: "新终态"})
	if err != nil {
		t.Fatalf("reserve with dead letter at capacity: %v", err)
	}
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if len(outbox.entries) != outbox.maxEntries || outbox.entryLocked(old.ID) != nil ||
		outbox.entryLocked(fresh.ID) == nil {
		t.Fatalf("entries after dead-letter eviction=%#v", outbox.entries)
	}
}

func TestTerminalOutboxDueIDsUseRetryOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	first, _ := outbox.enqueue(terminalOutboxDraft{Route: route, Text: "first"})
	second, _ := outbox.enqueue(terminalOutboxDraft{Route: route, Text: "second"})
	outbox.mu.Lock()
	outbox.entryLocked(first.ID).NextAttempt = base.Add(-time.Second)
	outbox.entryLocked(second.ID).NextAttempt = base.Add(-time.Minute)
	outbox.mu.Unlock()
	ids := outbox.dueIDs()
	if len(ids) != 2 || ids[0] != second.ID || ids[1] != first.ID {
		t.Fatalf("due ids=%#v", ids)
	}
}

func TestTerminalOutboxCancelledWorkerDoesNotMutateDueEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	entry, err := outbox.enqueue(terminalOutboxDraft{Route: route, Text: "result"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outbox.run(ctx)
	loaded, err := loadTerminalOutbox(path)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	if loaded[0].ID != entry.ID || loaded[0].Attempts != 0 || loaded[0].LastError != "" {
		t.Fatalf("cancelled worker mutated entry: %#v", loaded[0])
	}
}

func TestTerminalOutboxRejectsCorruptBroadAndSymlinkFiles(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "outbox.json")
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTerminalOutbox(path); err == nil {
			t.Fatal("corrupt outbox must fail closed")
		}
	})
	t.Run("broad permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "outbox.json")
		if err := os.WriteFile(path, []byte(`{"version":1,"entries":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTerminalOutbox(path); err == nil {
			t.Fatal("broad outbox permissions must fail closed")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		link := filepath.Join(dir, "outbox.json")
		if err := os.WriteFile(target, []byte(`{"version":1,"entries":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTerminalOutbox(link); err == nil {
			t.Fatal("symlink outbox must fail closed")
		}
	})
}

func TestTerminalOutboxPersistenceFailureDoesNotCommitMemoryEntry(t *testing.T) {
	dir := t.TempDir()
	blockedParent := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockedParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outbox := &terminalOutbox{
		path: filepath.Join(blockedParent, "outbox.json"), registry: platform.NewRegistry(nil),
		processing: make(map[string]bool), wake: make(chan struct{}, 1), now: time.Now,
	}
	route := platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "bot-1", ChatID: "wx-user"}
	if _, err := outbox.enqueue(terminalOutboxDraft{Route: route, Text: "不应提交"}); err == nil {
		t.Fatal("persistence failure must reject enqueue")
	}
	if len(outbox.entries) != 0 {
		t.Fatalf("entries=%#v, failed persistence must roll back memory", outbox.entries)
	}
}

func TestTerminalOutboxMarkDeliveredRollsBackMemoryOnPersistenceFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := outbox.enqueue(terminalOutboxDraft{
		Route: platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "bot-1", ChatID: "wx-user"},
		Text:  "result",
	})
	if err != nil {
		t.Fatal(err)
	}
	before := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	outbox.path = filepath.Join(path, "blocked")

	if err := outbox.markDelivered(entry.ID, terminalOutboxTextStage); err == nil {
		t.Fatal("markDelivered error=nil, want persistence failure")
	}
	after := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("entry changed after failed persistence:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestTerminalOutboxRecordFailureRollsBackMemoryOnPersistenceFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := outbox.enqueue(terminalOutboxDraft{
		Route: platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "bot-1", ChatID: "wx-user"},
		Text:  "result",
	})
	if err != nil {
		t.Fatal(err)
	}
	before := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	outbox.path = filepath.Join(path, "blocked")

	if err := outbox.recordFailure(entry.ID, errors.New("delivery failed")); err == nil {
		t.Fatal("recordFailure error=nil, want persistence failure")
	}
	after := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("entry changed after failed persistence:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestTerminalOutboxRemoveDeliveredRollsBackMemoryOnPersistenceFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := outbox.enqueue(terminalOutboxDraft{
		Route: platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "bot-1", ChatID: "wx-user"},
		Text:  "result",
	})
	if err != nil {
		t.Fatal(err)
	}
	before := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	outbox.path = filepath.Join(path, "blocked")

	if err := outbox.removeDelivered(entry.ID); err == nil {
		t.Fatal("removeDelivered error=nil, want persistence failure")
	}
	after := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("entry changed after failed persistence:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestTerminalOutboxRefreshTraceRollsBackMemoryOnPersistenceFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot-1", ChatID: "chat-1",
	}
	reference := testDurableStreamReference("card-1")
	entry, err := outbox.reserve(terminalOutboxDraft{
		Route: route, Stream: &reference, Text: "任务已中断",
		Trace: observability.TraceContext{TraceID: "trace-old", ThreadID: "thread-old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	before := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	outbox.now = func() time.Time { return before.UpdatedAt.Add(time.Hour) }
	outbox.path = filepath.Join(path, "blocked")

	err = outbox.refreshStreamReservationTrace(entry.ID, observability.TraceContext{
		TraceID: "trace-new", ThreadID: "thread-new",
	})
	if err == nil {
		t.Fatal("refresh trace error=nil, want persistence failure")
	}
	after := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("entry changed after failed trace refresh:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestTerminalOutboxDetachRollsBackFollowerHoldOnPersistenceFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot-1", ChatID: "chat-1",
	}
	reference := testDurableStreamReference("card-1")
	entry, err := outbox.reserve(terminalOutboxDraft{
		Route: route, AgentName: "codex", Stream: &reference, Text: "任务已中断",
	})
	if err != nil {
		t.Fatal(err)
	}
	outbox.mu.Lock()
	outbox.followerHeld[entry.ID] = true
	outbox.mu.Unlock()
	before := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	outbox.path = filepath.Join(path, "blocked")

	err = outbox.detachStreamReservation(entry.ID, testPendingStreamSupersede(
		"00000000-0000-4000-8000-000000000120", route, "card-1", time.Now(),
	))
	if err == nil {
		t.Fatal("detach error=nil, want persistence failure")
	}
	after := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	outbox.mu.Lock()
	preparing := outbox.preparing[entry.ID]
	held := outbox.followerHeld[entry.ID]
	outbox.mu.Unlock()
	if !reflect.DeepEqual(after, before) || !preparing || !held {
		t.Fatalf("detach rollback entry=%#v preparing=%v followerHeld=%v", after, preparing, held)
	}
}

func TestTerminalOutboxRestartRetriesTextWithSameIdempotencyKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "bot-1", ChatID: "wx-user"}
	reply := newOutboxTestReplier(route)
	reply.failTextAfterAccept = 1
	registry := newOutboxTestRegistry(route, reply)
	first, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.enqueueAndAttempt(context.Background(), terminalOutboxDraft{Route: route, Text: "跨重启结果"}, reply); err != nil {
		t.Fatalf("enqueueAndAttempt: %v", err)
	}
	pending, err := loadTerminalOutbox(path)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	second, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.attempt(context.Background(), pending[0].ID, nil); err != nil {
		t.Fatalf("restart attempt: %v", err)
	}
	remaining, err := loadTerminalOutbox(path)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%#v err=%v", remaining, err)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if len(reply.textKeys) != 2 || reply.textKeys[0] != reply.textKeys[1] || !strings.HasSuffix(reply.textKeys[0], ":text") {
		t.Fatalf("text keys=%#v, want stable retry key", reply.textKeys)
	}
	if len(reply.accepted) != 1 {
		t.Fatalf("accepted=%#v, want one user-visible message", reply.accepted)
	}
}

func TestTerminalOutboxWorkerDeliversPendingEntryOnStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "bot-1", ChatID: "wx-user"}
	reply := newOutboxTestReplier(route)
	reply.textDelivered = make(chan string, 1)
	registry := newOutboxTestRegistry(route, reply)
	beforeRestart, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beforeRestart.enqueue(terminalOutboxDraft{Route: route, Text: "重启后投递"}); err != nil {
		t.Fatal(err)
	}
	afterRestart, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go afterRestart.run(ctx)
	select {
	case got := <-reply.textDelivered:
		if got != "重启后投递" {
			t.Fatalf("delivered=%q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup worker did not redeliver pending terminal entry")
	}
	deadline := time.After(2 * time.Second)
	for {
		remaining, err := loadTerminalOutbox(path)
		if err != nil {
			t.Fatalf("load remaining: %v", err)
		}
		if len(remaining) == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("remaining=%#v", remaining)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestTerminalOutboxReservedRecoveryDraftBecomesDeliverableAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "bot-1", ChatID: "wx-user"}
	reply := newOutboxTestReplier(route)
	registry := newOutboxTestRegistry(route, reply)
	beforeCrash, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := beforeCrash.reserve(terminalOutboxDraft{Route: route, AgentName: "codex", Text: "可恢复终态"})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if ids := beforeCrash.dueIDs(); len(ids) != 0 {
		t.Fatalf("reservation became deliverable before preparation finished: %#v", ids)
	}
	persisted, err := loadTerminalOutbox(path)
	if err != nil || len(persisted) != 1 || persisted[0].ID != reservation.ID || persisted[0].Text != "可恢复终态" {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}

	afterCrash, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := afterCrash.attempt(context.Background(), reservation.ID, nil); err != nil {
		t.Fatalf("restart attempt: %v", err)
	}
	remaining, err := loadTerminalOutbox(path)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%#v err=%v", remaining, err)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if len(reply.accepted) != 1 {
		t.Fatalf("accepted=%#v, want one recovered terminal text", reply.accepted)
	}
}

func TestProgressSessionRestartRecoversOriginalCardAsStopped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
	reply.finalOutside = true
	registry := newOutboxTestRegistry(route, reply)
	h := NewHandler(nil, nil)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	if err := h.StartTerminalOutbox(workerCtx, registry, path); err != nil {
		t.Fatalf("StartTerminalOutbox: %v", err)
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, _, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "claude", "/workspace/jumpserver", "检查审查文档", cfg,
	)
	stopWorker()
	progress.stopBackground()

	persisted, err := loadTerminalOutbox(path)
	if err != nil {
		t.Fatalf("load active task recovery: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("active task recovery entries=%d, want 1", len(persisted))
	}
	if !persisted[0].RichResult || persisted[0].ResultTitle != "Claude · jumpserver" {
		t.Fatalf("active result presentation=%#v, want Claude workspace result card", persisted[0])
	}

	restarted, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatalf("newTerminalOutbox after restart: %v", err)
	}
	if err := restarted.attempt(context.Background(), persisted[0].ID, nil); err != nil {
		t.Fatalf("recover active task card: %v", err)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if reply.checkpointCalls != 1 || len(reply.recoveredReferences) != 1 {
		t.Fatalf("checkpoint calls=%d recovered references=%d, want original-card recovery", reply.checkpointCalls, len(reply.recoveredReferences))
	}
	if reply.recoveredStates[0] != platform.StreamTerminalStopped {
		t.Fatalf("recovered state=%q, want stopped", reply.recoveredStates[0])
	}
	if got := string(reply.checkpointPayloadSeen[0]); !strings.Contains(got, "card-1") || !strings.Contains(got, "任务已中断") {
		t.Fatalf("recovered checkpoint=%s, want original card and interruption content", got)
	}
	if len(reply.accepted) != 1 || len(reply.results) != 1 || reply.results[0].State != platform.StreamTerminalStopped {
		t.Fatalf("fallback texts=%#v, want independent interruption result", reply.accepted)
	}
}

func TestTerminalOutboxRecoversStagedTerminalStateAndResult(t *testing.T) {
	tests := []struct {
		name    string
		failed  bool
		stopped bool
		text    string
		state   platform.StreamTerminalState
	}{
		{name: "completed", text: "完整最终回答", state: platform.StreamTerminalCompleted},
		{name: "failed", failed: true, text: "任务执行失败：boom", state: platform.StreamTerminalFailed},
		{name: "stopped", stopped: true, text: "任务已按请求停止。", state: platform.StreamTerminalStopped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "outbox.json")
			route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
			reply := newOutboxTestReplier(route)
			registry := newOutboxTestRegistry(route, reply)
			beforeCrash, err := newTerminalOutbox(path, registry)
			if err != nil {
				t.Fatal(err)
			}
			reference := &platform.DurableStreamReference{
				Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-1","sequence":7}`),
			}
			entry, err := beforeCrash.reserve(terminalOutboxDraft{
				Route: route, Stream: reference, Text: tc.text, Failed: tc.failed, Stopped: tc.stopped,
			})
			if err != nil {
				t.Fatal(err)
			}
			afterCrash, err := newTerminalOutbox(path, registry)
			if err != nil {
				t.Fatal(err)
			}
			if err := afterCrash.attempt(context.Background(), entry.ID, nil); err != nil {
				t.Fatalf("recover: %v", err)
			}
			reply.mu.Lock()
			defer reply.mu.Unlock()
			if len(reply.recoveredStates) != 1 || reply.recoveredStates[0] != tc.state {
				t.Fatalf("recovered states=%#v, want %q", reply.recoveredStates, tc.state)
			}
			if len(reply.accepted) != 1 {
				t.Fatalf("accepted=%#v, want one independent result", reply.accepted)
			}
			for _, delivered := range reply.accepted {
				if delivered != tc.text {
					t.Fatalf("delivered=%q, want %q", delivered, tc.text)
				}
			}
		})
	}
}

func TestProgressSessionRefreshesActiveRecoveryAfterProgressUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, reply), path); err != nil {
		t.Fatalf("StartTerminalOutbox: %v", err)
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, _, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "claude", "/workspace/jumpserver", "检查任务", cfg,
	)
	progress.onTaskProgress(taskProgressUpdate{
		latest: "正在检查。", card: "**执行进度**\n\n正在检查。", timeline: true, commentary: true,
		timelineItems: []agent.ProgressEvent{{
			ID: "agent-message:first", Kind: agent.ProgressKindCommentary,
			State: agent.ProgressStateCompleted, Text: "正在检查。",
		}},
	})
	waitUntil(t, func() bool {
		persisted, err := loadTerminalOutbox(path)
		return err == nil && len(persisted) == 1 && persisted[0].Stream != nil &&
			strings.Contains(string(persisted[0].Stream.Payload), "正在检查")
	})

	persisted, err := loadTerminalOutbox(path)
	if err != nil || len(persisted) != 1 || persisted[0].Stream == nil ||
		!strings.Contains(string(persisted[0].Stream.Payload), "正在检查") {
		t.Fatalf("persisted=%#v err=%v, want latest progress in recovery reference", persisted, err)
	}
}

func TestProgressSessionRefreshesActiveRecoveryAfterAdapterStateChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, reply), path); err != nil {
		t.Fatalf("StartTerminalOutbox: %v", err)
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, _, _ = h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "claude", "/workspace/jumpserver", "检查审批", cfg,
	)

	reply.stream.changeDurableReference("审批：允许本次")
	waitUntil(t, func() bool {
		persisted, err := loadTerminalOutbox(path)
		return err == nil && len(persisted) == 1 && persisted[0].Stream != nil &&
			strings.Contains(string(persisted[0].Stream.Payload), "审批：允许本次")
	})
}

func TestTerminalOutboxKeepsStreamRecoveryObservableWhenAdapterCannotPrepareCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	reference := &platform.DurableStreamReference{Kind: "test.stream.v1", Payload: json.RawMessage(`{"card_id":"card-1"}`)}
	entry, err := outbox.enqueue(terminalOutboxDraft{Route: route, Stream: reference, Text: "最终结果"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = outbox.prepareStreamRecovery(entry.ID, entry, platformtest.NewReplier(platform.Capabilities{Text: true}))
	if !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("prepareStreamRecovery error=%v, want ErrUnsupported", err)
	}
}

func TestDirectProgressTerminalClearsActiveRecoveryReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, reply), path); err != nil {
		t.Fatalf("StartTerminalOutbox: %v", err)
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, _, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "claude", "/workspace/jumpserver", "检查审查文档", cfg,
	)
	if entries, err := loadTerminalOutbox(path); err != nil || len(entries) != 1 {
		t.Fatalf("active recovery entries=%#v err=%v", entries, err)
	}
	_ = progress.stopWithTerminal("", false, true)
	if entries, err := loadTerminalOutbox(path); err != nil || len(entries) != 0 {
		t.Fatalf("terminal progress left stale recovery entries=%#v err=%v", entries, err)
	}
}

func TestTerminalOutboxDoesNotReplayCompletedCheckpointAfterResultFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.failTextAfterAccept = 1
	registry := newOutboxTestRegistry(route, reply)
	outbox, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &platform.TerminalCheckpoint{Kind: "test.terminal.v1", Payload: json.RawMessage(`{"card_id":"card-1"}`)}
	if err := outbox.enqueueAndAttempt(context.Background(), terminalOutboxDraft{
		Route: route, Checkpoint: checkpoint, Text: "任务执行失败：boom",
	}, reply); err != nil {
		t.Fatal(err)
	}
	pending, err := loadTerminalOutbox(path)
	if err != nil || len(pending) != 1 || !pending[0].CheckpointDelivered || pending[0].TextDelivered {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	restarted, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.attempt(context.Background(), pending[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if reply.checkpointCalls != 1 {
		t.Fatalf("checkpoint calls=%d, want one", reply.checkpointCalls)
	}
	if len(reply.textKeys) != 2 || reply.textKeys[0] != reply.textKeys[1] || len(reply.accepted) != 1 {
		t.Fatalf("keys=%#v accepted=%#v", reply.textKeys, reply.accepted)
	}
}

func TestTerminalOutboxDeliversResultWhenCheckpointFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.failCheckpoint = 1
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &platform.TerminalCheckpoint{Kind: "test.terminal.v1", Payload: json.RawMessage(`{"state":"stopped"}`)}
	if err := outbox.enqueueAndAttempt(context.Background(), terminalOutboxDraft{
		Route: route, Stopped: true, Checkpoint: checkpoint,
		Text: "任务已按请求停止。",
	}, reply); err != nil {
		t.Fatal(err)
	}
	pending, err := loadTerminalOutbox(path)
	if err != nil || len(pending) != 1 || pending[0].CheckpointDelivered || !pending[0].TextDelivered {
		t.Fatalf("pending=%#v err=%v, failed checkpoint must not block result delivery", pending, err)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if len(reply.accepted) != 1 || len(reply.textKeys) != 1 {
		t.Fatalf("accepted=%#v keys=%#v, want one independent stopped result", reply.accepted, reply.textKeys)
	}
}

func TestTerminalOutboxStartsResultDeliveryWhileCheckpointIsBlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	checkpointStarted := make(chan struct{})
	releaseCheckpoint := make(chan struct{})
	reply.beforeCheckpoint = func() {
		close(checkpointStarted)
		<-releaseCheckpoint
	}
	reply.textDelivered = make(chan string, 1)
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &platform.TerminalCheckpoint{Kind: "test.terminal.v1", Payload: json.RawMessage(`{"card_id":"card-1"}`)}
	entry, err := outbox.enqueue(terminalOutboxDraft{Route: route, Checkpoint: checkpoint, Text: "完整最终结果"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- outbox.attempt(context.Background(), entry.ID, reply)
	}()
	select {
	case <-checkpointStarted:
	case <-time.After(time.Second):
		close(releaseCheckpoint)
		t.Fatal("checkpoint delivery did not start")
	}
	deliveredBeforeCheckpoint := false
	select {
	case text := <-reply.textDelivered:
		deliveredBeforeCheckpoint = text == "完整最终结果"
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseCheckpoint)
	if err := <-done; err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if !deliveredBeforeCheckpoint {
		t.Fatal("blocked checkpoint delayed the independent final result")
	}
}

func TestTerminalOutboxPreservesTraceAcrossDurableDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "bot-1", ChatID: "wx-user"}
	reply := newOutboxTestReplier(route)
	capture := &traceCapture{}
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply), capture)
	if err != nil {
		t.Fatal(err)
	}
	trace := observability.NewTraceContext(observability.TraceSeed{
		Platform: string(route.Platform), AccountID: route.AccountID, ChatID: route.ChatID,
		MessageID: "message-1", RouteKey: "private-route-key",
	}).WithAgent("codex").WithTask("task-1")
	if err := outbox.enqueueAndAttempt(context.Background(), terminalOutboxDraft{
		Route: route, AgentName: "codex", Text: "最终结果", Trace: trace,
	}, reply); err != nil {
		t.Fatal(err)
	}

	events := capture.snapshot()
	wantStages := []string{"terminal.outbox.enqueued", "terminal.delivery.attempt", "terminal.delivery.completed"}
	if len(events) != len(wantStages) {
		t.Fatalf("events=%#v", events)
	}
	for index, wantStage := range wantStages {
		if events[index].Stage != wantStage || events[index].TraceID != trace.TraceID || events[index].TaskID != "task-1" {
			t.Fatalf("event[%d]=%#v, want stage=%q trace=%q task=task-1", index, events[index], wantStage, trace.TraceID)
		}
		if events[index].RouteHash != "" {
			t.Fatalf("outbox trace event must not reconstruct an in-memory route key: %#v", events[index])
		}
	}
}

func TestFinishProgressReplyPersistsCheckpointBeforeTerminalDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
	reply.finalOutside = true
	prepareReached := make(chan struct{})
	continuePrepare := make(chan struct{})
	reply.stream.beforePrepare = func() {
		close(prepareReached)
		<-continuePrepare
	}
	checkpointReached := make(chan struct{})
	continueCheckpoint := make(chan struct{})
	reply.beforeCheckpoint = func() {
		close(checkpointReached)
		<-continueCheckpoint
	}
	registry := newOutboxTestRegistry(route, reply)
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.StartTerminalOutbox(ctx, registry, path); err != nil {
		t.Fatalf("StartTerminalOutbox: %v", err)
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, finish, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/weclaw", "运行发布检查", cfg,
	)
	finished := make(chan bool, 1)
	go func() {
		finished <- h.finishAndSendProgressReply(progressReplyDelivery{
			delivery: replyDeliveryRequest{
				ctx: context.Background(), replyWriter: reply, userID: "user-1", agentName: "codex", reply: "发布检查已通过",
			},
			finish: finish, progress: progress,
		})
	}()
	<-prepareReached
	recoveryEntries, recoveryErr := loadTerminalOutbox(path)
	observedRecoveryDraft := recoveryErr == nil &&
		len(recoveryEntries) == 1 &&
		recoveryEntries[0].Checkpoint == nil &&
		recoveryEntries[0].Stream != nil &&
		recoveryEntries[0].Text == "发布检查已通过" &&
		recoveryEntries[0].RichResult &&
		recoveryEntries[0].ResultTitle == "Codex · weclaw" &&
		!recoveryEntries[0].Failed && !recoveryEntries[0].Stopped
	close(continuePrepare)

	<-checkpointReached
	checkpointEntries, checkpointErr := loadTerminalOutbox(path)
	observedPersisted := checkpointErr == nil &&
		len(checkpointEntries) == 1 &&
		checkpointEntries[0].Checkpoint != nil &&
		checkpointEntries[0].Text == "发布检查已通过" &&
		checkpointEntries[0].RichResult &&
		checkpointEntries[0].ResultTitle == "Codex · weclaw" &&
		!checkpointEntries[0].CheckpointDelivered
	close(continueCheckpoint)
	consumed := <-finished
	if consumed {
		t.Fatal("independent final reply must not be consumed by durable card checkpoint")
	}
	if !observedRecoveryDraft {
		t.Fatalf("stream was frozen before durable recovery draft: observed=%v entries=%#v err=%v",
			observedRecoveryDraft, recoveryEntries, recoveryErr)
	}
	if !observedPersisted {
		t.Fatalf("checkpoint was delivered before durable persistence: observed=%v entries=%#v err=%v",
			observedPersisted, checkpointEntries, checkpointErr)
	}
	waitForTerminalOutboxEmpty(t, path)
	remaining, err := loadTerminalOutbox(path)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%#v err=%v", remaining, err)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if reply.checkpointCalls != 1 || len(reply.accepted) != 1 || len(reply.results) != 1 ||
		reply.results[0].Title != "Codex · weclaw" || reply.results[0].State != platform.StreamTerminalCompleted ||
		len(reply.checkpointPayloadSeen) != 1 ||
		strings.Contains(string(reply.checkpointPayloadSeen[0]), "发布检查已通过") {
		t.Fatalf("checkpoint calls=%d accepted=%#v results=%#v payloads=%q", reply.checkpointCalls, reply.accepted, reply.results, reply.checkpointPayloadSeen)
	}
	if len(reply.recoveredReferences) != 0 {
		t.Fatalf("normal completion recovered stale stream references: %#v", reply.recoveredReferences)
	}
}

func TestFinishStoppedProgressPersistsStoppedCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
	reply.finalOutside = true
	registry := newOutboxTestRegistry(route, reply)
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.StartTerminalOutbox(ctx, registry, path); err != nil {
		t.Fatalf("StartTerminalOutbox: %v", err)
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, finish, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/weclaw", "运行任务", cfg,
	)

	consumed := h.finishAndSendProgressReply(progressReplyDelivery{
		delivery: replyDeliveryRequest{
			ctx: context.Background(), replyWriter: reply, userID: "user-1",
			agentName: "codex", reply: "任务已按请求停止。",
		},
		stopped: true, finish: finish, progress: progress,
	})
	if consumed {
		t.Fatal("stopped result must remain outside durable checkpoint")
	}
	waitForTerminalOutboxEmpty(t, path)
	reply.stream.mu.Lock()
	state := reply.stream.terminalState
	reply.stream.mu.Unlock()
	if state != platform.StreamTerminalStopped {
		t.Fatalf("terminal state=%q, want stopped", state)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if len(reply.checkpointPayloadSeen) != 1 ||
		!strings.Contains(string(reply.checkpointPayloadSeen[0]), `"state":"stopped"`) ||
		strings.Contains(string(reply.checkpointPayloadSeen[0]), `"failed":true`) ||
		strings.Contains(string(reply.checkpointPayloadSeen[0]), "任务已按请求停止") ||
		len(reply.accepted) != 1 {
		t.Fatalf("checkpoint payloads=%q, want stopped non-failure state", reply.checkpointPayloadSeen)
	}
}

func TestFinishProgressReplyDoesNotPersistStatusSentinelWhenCheckpointPreparationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "bot-1", ChatID: "wx-user"}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{prepareErr: errors.New("checkpoint unavailable")}
	reply.textDelivered = make(chan string, 1)
	registry := newOutboxTestRegistry(route, reply)
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.StartTerminalOutbox(ctx, registry, path); err != nil {
		t.Fatalf("StartTerminalOutbox: %v", err)
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, finish, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/weclaw", "运行发布检查", cfg,
	)

	consumed := h.finishAndSendProgressReply(progressReplyDelivery{
		delivery: replyDeliveryRequest{
			ctx: context.Background(), replyWriter: reply, userID: "user-1", agentName: "codex", reply: progressStatusOnlyComplete,
		},
		finish: finish, progress: progress,
	})
	if consumed {
		t.Fatal("status-only fallback should not be reported as consumed by a checkpoint")
	}
	select {
	case text := <-reply.textDelivered:
		if text != progressDefaultCompletion || strings.ContainsRune(text, '\x00') {
			t.Fatalf("recovery text=%q, want default completion without internal sentinel", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovery text was not delivered")
	}
	waitForTerminalOutboxEmpty(t, path)
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if len(reply.recoveredReferences) != 1 || reply.checkpointCalls != 1 {
		t.Fatalf("recovered references=%#v checkpoint calls=%d, want card recovery after preparation failure", reply.recoveredReferences, reply.checkpointCalls)
	}
}

func testPendingStreamSupersede(id string, route platform.DeliveryRoute, cardID string, nextAttempt time.Time) pendingStreamSupersede {
	payload, _ := json.Marshal(map[string]string{"card_id": cardID})
	return pendingStreamSupersede{
		ID: id, Route: route,
		Checkpoint:  platform.SupersedeCheckpoint{Kind: "test.supersede.v1", Payload: payload},
		NextAttempt: nextAttempt,
	}
}

func testDurableStreamReference(cardID string) platform.DurableStreamReference {
	payload, _ := json.Marshal(map[string]any{"card_id": cardID, "sequence": 7})
	return platform.DurableStreamReference{Kind: "test.stream.v1", Payload: payload}
}

func TestTerminalOutboxReanchorPersistsNewAuthorityAndPendingSupersedeAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	oldRoute := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "old-chat"}
	newRoute := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "new-chat"}
	outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	oldReference := testDurableStreamReference("card-old")
	entry, err := outbox.reserve(terminalOutboxDraft{Route: oldRoute, Stream: &oldReference, Text: "任务已中断"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	newReference := testDurableStreamReference("card-new")
	pending := testPendingStreamSupersede("00000000-0000-4000-8000-000000000101", oldRoute, "card-old", base)
	if err := outbox.reanchorStreamReservation(entry.ID, newRoute, newReference, pending); err != nil {
		t.Fatalf("reanchorStreamReservation: %v", err)
	}
	if err := outbox.reanchorStreamReservation(entry.ID, newRoute, newReference, pending); err != nil {
		t.Fatalf("idempotent reanchorStreamReservation: %v", err)
	}
	loaded, err := loadTerminalOutbox(path)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	if loaded[0].Route != newRoute || loaded[0].Stream == nil || !strings.Contains(string(loaded[0].Stream.Payload), "card-new") ||
		len(loaded[0].PendingSupersedes) != 1 || loaded[0].PendingSupersedes[0].ID != pending.ID {
		t.Fatalf("reanchored entry=%#v", loaded[0])
	}
	if err := outbox.refreshStreamReservation(entry.ID, newRoute, newReference); err != nil {
		t.Fatalf("refreshStreamReservation: %v", err)
	}
	if refreshed := outbox.entryLocked(entry.ID); len(refreshed.PendingSupersedes) != 1 || refreshed.PendingSupersedes[0].ID != pending.ID {
		t.Fatalf("refresh cleared pending supersede: %#v", refreshed)
	}

	beforeMemory := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	beforeDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	originalPath := outbox.path
	outbox.path = filepath.Join(path, "blocked")
	failedReference := testDurableStreamReference("card-never-committed")
	failedPending := testPendingStreamSupersede("00000000-0000-4000-8000-000000000102", newRoute, "card-new", base)
	if err := outbox.reanchorStreamReservation(entry.ID, oldRoute, failedReference, failedPending); err == nil {
		t.Fatal("reanchor persistence failure returned nil")
	}
	outbox.path = originalPath
	afterMemory := cloneTerminalOutboxEntry(outbox.entryLocked(entry.ID))
	afterDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterMemory, beforeMemory) || !reflect.DeepEqual(afterDisk, beforeDisk) {
		t.Fatalf("failed reanchor changed authority:\nbefore=%#v\nafter=%#v", beforeMemory, afterMemory)
	}
}

func TestTerminalOutboxStreamReanchorHoldBlocksPendingDeliveryUntilAuthoritySwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	oldReference := testDurableStreamReference("card-old")
	entry, err := outbox.reserve(terminalOutboxDraft{Route: route, Stream: &oldReference, Text: "任务已中断"})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.beginStreamReanchor(entry.ID); err != nil {
		t.Fatal(err)
	}
	pending := testPendingStreamSupersede("00000000-0000-4000-8000-000000000121", route, "card-old", base)
	if err := outbox.reanchorStreamReservation(entry.ID, route, testDurableStreamReference("card-new"), pending); err != nil {
		outbox.endStreamReanchor(entry.ID)
		t.Fatal(err)
	}
	if err := outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, pending.ID); err != nil {
		outbox.endStreamReanchor(entry.ID)
		t.Fatalf("blocked attempt returned error: %v", err)
	}
	reply.mu.Lock()
	callsWhileHeld := reply.supersedeCalls
	reply.mu.Unlock()
	if callsWhileHeld != 0 {
		outbox.endStreamReanchor(entry.ID)
		t.Fatalf("supersede delivered before authority switch: calls=%d", callsWhileHeld)
	}
	outbox.endStreamReanchor(entry.ID)
	if err := outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, pending.ID); err != nil {
		t.Fatalf("supersede after authority switch: %v", err)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if reply.supersedeCalls != 1 {
		t.Fatalf("supersede calls=%d, want 1 after releasing reanchor hold", reply.supersedeCalls)
	}
}

func TestTerminalOutboxRetriesSupersedeWhileReservationIsPreparing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.failSupersede = 1
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	reference := testDurableStreamReference("card-old")
	entry, err := outbox.reserve(terminalOutboxDraft{Route: route, Stream: &reference, Text: "任务已中断"})
	if err != nil {
		t.Fatal(err)
	}
	newReference := testDurableStreamReference("card-new")
	pending := testPendingStreamSupersede("00000000-0000-4000-8000-000000000103", route, "card-old", base)
	if err := outbox.reanchorStreamReservation(entry.ID, route, newReference, pending); err != nil {
		t.Fatal(err)
	}
	if due := outbox.duePendingStreamSupersedes(); len(due) != 1 || due[0].entryID != entry.ID || due[0].pendingID != pending.ID {
		t.Fatalf("pending supersede was not due while reservation prepared: %#v", due)
	}
	if err := outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, pending.ID); err == nil {
		t.Fatal("first supersede attempt unexpectedly succeeded")
	}
	stored := outbox.entryLocked(entry.ID)
	if !outbox.preparing[entry.ID] || stored.Attempts != 0 || len(stored.PendingSupersedes) != 1 || stored.PendingSupersedes[0].Attempts != 1 {
		t.Fatalf("entry=%#v preparing=%v", stored, outbox.preparing[entry.ID])
	}
	if err := outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, pending.ID); err != nil {
		t.Fatalf("retry supersede: %v", err)
	}
	stored = outbox.entryLocked(entry.ID)
	if stored == nil || len(stored.PendingSupersedes) != 0 || stored.Stream == nil || !outbox.preparing[entry.ID] {
		t.Fatalf("active reservation after supersede=%#v", stored)
	}
}

func TestTerminalOutboxRestartReplaysPendingSupersedeBeforeTerminalRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	registry := newOutboxTestRegistry(route, reply)
	beforeRestart, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	beforeRestart.now = func() time.Time { return base }
	oldReference := testDurableStreamReference("card-old")
	entry, err := beforeRestart.reserve(terminalOutboxDraft{Route: route, Stream: &oldReference, Text: "任务已中断"})
	if err != nil {
		t.Fatal(err)
	}
	newReference := testDurableStreamReference("card-new")
	pending := testPendingStreamSupersede("00000000-0000-4000-8000-000000000104", route, "card-old", base)
	if err := beforeRestart.reanchorStreamReservation(entry.ID, route, newReference, pending); err != nil {
		t.Fatal(err)
	}

	restarted, err := newTerminalOutbox(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go restarted.run(ctx)
	waitForTerminalOutboxEmpty(t, path)
	cancel()
	reply.mu.Lock()
	order := append([]string(nil), reply.deliveryOrder...)
	reply.mu.Unlock()
	if len(order) < 2 || order[0] != "supersede" || order[1] != "prepare_terminal" {
		t.Fatalf("delivery order=%#v, want supersede before terminal recovery", order)
	}
}

func TestTerminalOutboxSupersedeFailureDoesNotConsumeTerminalAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.failSupersede = 1
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	outbox.maxAttempts = 1
	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	reference := testDurableStreamReference("card-old")
	entry, err := outbox.reserve(terminalOutboxDraft{Route: route, Stream: &reference, Text: "任务已中断"})
	if err != nil {
		t.Fatal(err)
	}
	pending := testPendingStreamSupersede("00000000-0000-4000-8000-000000000105", route, "card-old", base)
	if err := outbox.reanchorStreamReservation(entry.ID, route, testDurableStreamReference("card-new"), pending); err != nil {
		t.Fatal(err)
	}
	if err := outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, pending.ID); err == nil {
		t.Fatal("supersede failure returned nil")
	}
	stored := outbox.entryLocked(entry.ID)
	if stored.Attempts != 0 || stored.DeadLetter || len(stored.PendingSupersedes) != 1 || !stored.PendingSupersedes[0].DeadLetter || stored.PendingSupersedes[0].Attempts != 1 {
		t.Fatalf("entry=%#v", stored)
	}
}

func TestTerminalOutboxLoadsVersionOneEntryWithoutPendingSupersedes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	legacy := `{"version":1,"entries":[{"id":"00000000-0000-4000-8000-000000000106","route":{"platform":"feishu","chat_id":"oc_chat"},"text":"旧版结果","created_at":"2026-08-08T10:00:00Z","updated_at":"2026-08-08T10:00:00Z","next_attempt":"2026-08-08T10:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTerminalOutbox(path)
	if err != nil || len(loaded) != 1 || len(loaded[0].PendingSupersedes) != 0 || loaded[0].Text != "旧版结果" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestTerminalOutboxKeepsDeliveredTerminalWhileSupersedeNeedsRedrive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.failSupersede = 1
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	outbox.maxAttempts = 1
	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	reference := testDurableStreamReference("card-old")
	entry, err := outbox.reserve(terminalOutboxDraft{Route: route, Stream: &reference, Text: "任务已中断"})
	if err != nil {
		t.Fatal(err)
	}
	pending := testPendingStreamSupersede("00000000-0000-4000-8000-000000000107", route, "card-old", base)
	if err := outbox.reanchorStreamReservation(entry.ID, route, testDurableStreamReference("card-new"), pending); err != nil {
		t.Fatal(err)
	}
	checkpoint := &platform.TerminalCheckpoint{Kind: "test.terminal.v1", Payload: json.RawMessage(`{"card_id":"card-new"}`)}
	if err := outbox.stageReservationResult(entry.ID, terminalOutboxDraft{Route: route, Text: "最终结果"}); err != nil {
		t.Fatal(err)
	}
	if staged := outbox.entryLocked(entry.ID); len(staged.PendingSupersedes) != 1 {
		t.Fatalf("staging cleared pending supersede: %#v", staged)
	}
	if err := outbox.commitReservation(entry.ID, terminalOutboxDraft{Route: route, Checkpoint: checkpoint, Text: "最终结果"}); err != nil {
		t.Fatal(err)
	}
	if err := outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, pending.ID); err == nil {
		t.Fatal("supersede failure returned nil")
	}
	if err := outbox.attempt(context.Background(), entry.ID, reply); err != nil {
		t.Fatalf("terminal attempt: %v", err)
	}
	stored := outbox.entryLocked(entry.ID)
	if stored == nil || !stored.CheckpointDelivered || !stored.TextDelivered || len(stored.PendingSupersedes) != 1 || !stored.PendingSupersedes[0].DeadLetter {
		t.Fatalf("stored=%#v", stored)
	}
	status := outbox.status()
	if status.Pending != 0 || status.DeadLetter != 1 {
		t.Fatalf("status=%#v, want only supersede dead letter", status)
	}
	statusJSON, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(statusJSON), "oc_chat") || strings.Contains(string(statusJSON), "card-old") {
		t.Fatalf("status leaked route or checkpoint payload: %s", statusJSON)
	}
	if result, err := outbox.redrive(entry.ID); err != nil || result.Requested != 1 {
		t.Fatalf("redrive result=%#v err=%v", result, err)
	}
	if err := outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, pending.ID); err != nil {
		t.Fatalf("supersede redrive: %v", err)
	}
	if remaining := outbox.entryLocked(entry.ID); remaining != nil {
		t.Fatalf("delivered entry remained after supersede: %#v", remaining)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if reply.checkpointCalls != 1 || len(reply.accepted) != 1 || reply.supersedeCalls != 2 {
		t.Fatalf("checkpoint=%d accepted=%#v supersede=%d", reply.checkpointCalls, reply.accepted, reply.supersedeCalls)
	}
}

func TestTerminalOutboxRecordsSupersedeLifecycleWithoutCheckpointPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	capture := &traceCapture{}
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply), capture)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	outbox.now = func() time.Time { return base }
	trace := observability.NewTraceContext(observability.TraceSeed{Platform: string(route.Platform), ChatID: route.ChatID}).WithTask("task-1")
	reference := testDurableStreamReference("card-old")
	entry, err := outbox.reserve(terminalOutboxDraft{Route: route, Stream: &reference, Text: "任务已中断", Trace: trace})
	if err != nil {
		t.Fatal(err)
	}
	first := testPendingStreamSupersede("00000000-0000-4000-8000-000000000108", route, "card-old-secret-guide", base)
	reply.failSupersede = 1
	outbox.maxAttempts = 2
	if err := outbox.reanchorStreamReservation(entry.ID, route, testDurableStreamReference("card-new"), first); err != nil {
		t.Fatal(err)
	}
	_ = outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, first.ID)
	if err := outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	second := testPendingStreamSupersede("00000000-0000-4000-8000-000000000109", route, "card-new-secret-guide", base)
	reply.failSupersede = 1
	outbox.maxAttempts = 1
	if err := outbox.reanchorStreamReservation(entry.ID, route, testDurableStreamReference("card-latest"), second); err != nil {
		t.Fatal(err)
	}
	_ = outbox.attemptPendingStreamSupersede(context.Background(), entry.ID, second.ID)

	var stages []string
	for _, event := range capture.snapshot() {
		if strings.HasPrefix(event.Stage, "task.card_supersede") {
			stages = append(stages, event.Stage)
			if strings.Contains(event.Summary, "secret-guide") {
				t.Fatalf("trace leaked checkpoint payload: %#v", event)
			}
		}
	}
	want := []string{
		"task.card_supersede_pending", "task.card_supersede_retry", "task.card_superseded",
		"task.card_supersede_pending", "task.card_supersede_dead_letter",
	}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages=%#v, want %#v", stages, want)
	}
}

func TestTerminalOutboxPendingSupersedeMutationsRollbackOnPersistenceFailure(t *testing.T) {
	setup := func(t *testing.T) (*terminalOutbox, string, string, string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "outbox.json")
		route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
		outbox, err := newTerminalOutbox(path, platform.NewRegistry(nil))
		if err != nil {
			t.Fatal(err)
		}
		base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
		outbox.now = func() time.Time { return base }
		reference := testDurableStreamReference("card-old")
		entry, err := outbox.reserve(terminalOutboxDraft{Route: route, Stream: &reference, Text: "任务已中断"})
		if err != nil {
			t.Fatal(err)
		}
		pending := testPendingStreamSupersede("00000000-0000-4000-8000-000000000110", route, "card-old", base)
		if err := outbox.reanchorStreamReservation(entry.ID, route, testDurableStreamReference("card-new"), pending); err != nil {
			t.Fatal(err)
		}
		return outbox, path, entry.ID, pending.ID
	}
	assertRollback := func(t *testing.T, outbox *terminalOutbox, path string, entryID string, mutate func() error) {
		t.Helper()
		before := cloneTerminalOutboxEntry(outbox.entryLocked(entryID))
		beforeDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		originalPath := outbox.path
		outbox.path = filepath.Join(path, "blocked")
		if err := mutate(); err == nil {
			t.Fatal("mutation persistence failure returned nil")
		}
		outbox.path = originalPath
		after := cloneTerminalOutboxEntry(outbox.entryLocked(entryID))
		afterDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterDisk, beforeDisk) {
			t.Fatalf("mutation escaped rollback:\nbefore=%#v\nafter=%#v", before, after)
		}
	}

	t.Run("complete", func(t *testing.T) {
		outbox, path, entryID, pendingID := setup(t)
		assertRollback(t, outbox, path, entryID, func() error {
			return outbox.completePendingStreamSupersede(entryID, pendingID)
		})
	})
	t.Run("record failure", func(t *testing.T) {
		outbox, path, entryID, pendingID := setup(t)
		assertRollback(t, outbox, path, entryID, func() error {
			return outbox.recordPendingStreamSupersedeFailure(entryID, pendingID, errors.New("delivery failed"))
		})
	})
	t.Run("redrive", func(t *testing.T) {
		outbox, path, entryID, pendingID := setup(t)
		outbox.mu.Lock()
		stored := outbox.entryLocked(entryID)
		stored.PendingSupersedes[0].Attempts = 1
		stored.PendingSupersedes[0].DeadLetter = true
		stored.PendingSupersedes[0].DeadLetterAt = outbox.now()
		if err := outbox.persistLocked(); err != nil {
			outbox.mu.Unlock()
			t.Fatal(err)
		}
		outbox.mu.Unlock()
		assertRollback(t, outbox, path, entryID, func() error {
			_, err := outbox.redrive(pendingID)
			return err
		})
	})
}
