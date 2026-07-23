package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

type outboxTestReplier struct {
	mu                    sync.Mutex
	route                 platform.DeliveryRoute
	accepted              map[string]string
	textKeys              []string
	failTextAfterAccept   int
	checkpointCalls       int
	failCheckpoint        int
	checkpointPayloadSeen []json.RawMessage
	stream                *outboxTestStream
	beforeCheckpoint      func()
	textDelivered         chan string
}

func newOutboxTestReplier(route platform.DeliveryRoute) *outboxTestReplier {
	return &outboxTestReplier{route: route, accepted: make(map[string]string)}
}

func (r *outboxTestReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true, Streaming: true, StreamCompletionNotification: true}
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
func (r *outboxTestReplier) DeliveryRoute() platform.DeliveryRoute { return r.route }
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
func (r *outboxTestReplier) DeliverTerminal(_ context.Context, checkpoint platform.TerminalCheckpoint) error {
	if r.beforeCheckpoint != nil {
		r.beforeCheckpoint()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpointCalls++
	r.checkpointPayloadSeen = append(r.checkpointPayloadSeen, append(json.RawMessage(nil), checkpoint.Payload...))
	if r.failCheckpoint > 0 {
		r.failCheckpoint--
		return errors.New("checkpoint unavailable")
	}
	return nil
}

type outboxTestStream struct {
	mu            sync.Mutex
	prepared      int
	updates       []string
	beforePrepare func()
	prepareErr    error
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
func (s *outboxTestStream) PrepareTerminal(content string, failed bool) (platform.TerminalCheckpoint, error) {
	if s.beforePrepare != nil {
		s.beforePrepare()
	}
	s.mu.Lock()
	s.prepared++
	s.mu.Unlock()
	if s.prepareErr != nil {
		return platform.TerminalCheckpoint{}, s.prepareErr
	}
	payload, err := json.Marshal(map[string]any{"content": content, "failed": failed})
	return platform.TerminalCheckpoint{Kind: "test.terminal.v1", Payload: payload}, err
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
	p.reply.route = route
	return p.reply
}

func newOutboxTestRegistry(route platform.DeliveryRoute, reply *outboxTestReplier) *platform.Registry {
	return platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &outboxTestPlatform{name: route.Platform, account: route.AccountID, reply: reply},
		Access:   platform.NewAccessControl([]string{"test-user"}),
	}})
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
	entry, err := outbox.enqueue(terminalOutboxDraft{Route: route, AgentName: "codex", Text: "最终结果"})
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
	if err != nil || len(loaded) != 1 || loaded[0].Text != "最终结果" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
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
	if len(reply.textKeys) != 2 || reply.textKeys[0] != reply.textKeys[1] {
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

func TestTerminalOutboxDoesNotReplayCompletedCheckpointAfterNotificationFailure(t *testing.T) {
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
		Route: route, Checkpoint: checkpoint, Notification: "任务执行失败，请查看上方卡片。",
	}, reply); err != nil {
		t.Fatal(err)
	}
	pending, err := loadTerminalOutbox(path)
	if err != nil || len(pending) != 1 || !pending[0].CheckpointDelivered || pending[0].NotificationDelivered {
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
	var observedRecoveryDraft bool
	var recoveryObserveErr error
	reply.stream.beforePrepare = func() {
		entries, err := loadTerminalOutbox(path)
		if err != nil {
			recoveryObserveErr = err
			return
		}
		observedRecoveryDraft = len(entries) == 1 &&
			entries[0].Checkpoint == nil &&
			entries[0].Text == "发布检查已通过"
	}
	var observedPersisted bool
	var observeErr error
	reply.beforeCheckpoint = func() {
		entries, err := loadTerminalOutbox(path)
		if err != nil {
			observeErr = err
			return
		}
		observedPersisted = len(entries) == 1 && entries[0].Checkpoint != nil && !entries[0].CheckpointDelivered
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
	consumed := h.finishAndSendProgressReply(progressReplyDelivery{
		delivery: replyDeliveryRequest{
			ctx: context.Background(), replyWriter: reply, userID: "user-1", agentName: "codex", reply: "发布检查已通过",
		},
		finish: finish, progress: progress,
	})
	if !consumed {
		t.Fatal("terminal reply should be consumed by durable card checkpoint")
	}
	if recoveryObserveErr != nil || !observedRecoveryDraft {
		t.Fatalf("stream was frozen before durable recovery draft: observed=%v err=%v", observedRecoveryDraft, recoveryObserveErr)
	}
	if observeErr != nil || !observedPersisted {
		t.Fatalf("checkpoint was delivered before durable persistence: observed=%v err=%v", observedPersisted, observeErr)
	}
	remaining, err := loadTerminalOutbox(path)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%#v err=%v", remaining, err)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if reply.checkpointCalls != 1 || len(reply.accepted) != 0 || len(reply.checkpointPayloadSeen) != 1 || !strings.Contains(string(reply.checkpointPayloadSeen[0]), "发布检查已通过") {
		t.Fatalf("checkpoint calls=%d accepted=%#v payloads=%q", reply.checkpointCalls, reply.accepted, reply.checkpointPayloadSeen)
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
}
