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
	terminalState platform.StreamTerminalState
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

func TestTerminalOutboxDoesNotNotifyBeforeStoppedCheckpointSucceeds(t *testing.T) {
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
		Notification: "任务已停止，请查看上方卡片。",
	}, reply); err != nil {
		t.Fatal(err)
	}
	pending, err := loadTerminalOutbox(path)
	if err != nil || len(pending) != 1 || pending[0].CheckpointDelivered || pending[0].NotificationDelivered {
		t.Fatalf("pending=%#v err=%v, failed checkpoint must keep notification pending", pending, err)
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if len(reply.accepted) != 0 || len(reply.textKeys) != 0 {
		t.Fatalf("accepted=%#v keys=%#v, notification must not precede stopped checkpoint", reply.accepted, reply.textKeys)
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
		recoveryEntries[0].Text == "发布检查已通过"
	close(continuePrepare)

	<-checkpointReached
	checkpointEntries, checkpointErr := loadTerminalOutbox(path)
	observedPersisted := checkpointErr == nil &&
		len(checkpointEntries) == 1 &&
		checkpointEntries[0].Checkpoint != nil &&
		!checkpointEntries[0].CheckpointDelivered
	close(continueCheckpoint)
	consumed := <-finished
	if !consumed {
		t.Fatal("terminal reply should be consumed by durable card checkpoint")
	}
	if !observedRecoveryDraft {
		t.Fatalf("stream was frozen before durable recovery draft: observed=%v entries=%#v err=%v",
			observedRecoveryDraft, recoveryEntries, recoveryErr)
	}
	if !observedPersisted {
		t.Fatalf("checkpoint was delivered before durable persistence: observed=%v entries=%#v err=%v",
			observedPersisted, checkpointEntries, checkpointErr)
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

func TestFinishStoppedProgressPersistsStoppedCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
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
	if !consumed {
		t.Fatal("stopped terminal reply should be consumed by durable checkpoint")
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
		strings.Contains(string(reply.checkpointPayloadSeen[0]), `"failed":true`) {
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
}
