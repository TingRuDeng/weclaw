package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRecordsAndQueriesSafeTraceFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "trace.jsonl")
	store, err := NewStore(StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	trace := NewTraceContext(TraceSeed{
		Platform: "feishu", AccountID: "app-1", ChatID: "chat-1",
		MessageID: "message-1", RouteKey: "feishu:tenant:dm:chat:user",
	}).WithClientID("client-1").WithAgent("codex").WithTask("task-1")
	event := EventFor(trace, "task.started", "running")
	event.Summary = "authorization: Bearer super-secret"
	if err := store.Record(event); err != nil {
		t.Fatal(err)
	}

	page, err := store.Query(context.Background(), Query{MessageID: "message-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events=%#v, want one", page.Events)
	}
	got := page.Events[0]
	if got.TraceID != trace.TraceID || got.TaskID != "task-1" || got.RouteHash == "" {
		t.Fatalf("event=%#v", got)
	}
	if strings.Contains(got.Summary, "super-secret") || strings.Contains(string(mustReadFile(t, path)), trace.RouteKey) {
		t.Fatalf("sensitive data leaked: event=%#v file=%q", got, mustReadFile(t, path))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o, want 0600", info.Mode().Perm())
	}
}

func TestStoreQueryKeepsNewestMatchingEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "trace.jsonl")
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store, err := NewStore(StoreOptions{Path: path, Now: func() time.Time {
		now = now.Add(time.Second)
		return now
	}})
	if err != nil {
		t.Fatal(err)
	}
	trace := NewTraceContext(TraceSeed{MessageID: "message-1"})
	for index := 0; index < 5; index++ {
		event := EventFor(trace, "task.progress", "running")
		event.Sequence = uint64(index + 1)
		if err := store.Record(event); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Query(context.Background(), Query{TraceID: trace.TraceID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || len(page.Events) != 2 || page.Events[0].Sequence != 4 || page.Events[1].Sequence != 5 {
		t.Fatalf("page=%#v", page)
	}
}

func TestNewStoreRejectsSymlinkAndBroadPermissions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "trace.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewStore(StoreOptions{Path: link}); err == nil {
		t.Fatal("symlink trace file was accepted")
	}

	broad := filepath.Join(dir, "broad.jsonl")
	if err := os.WriteFile(broad, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(broad, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(StoreOptions{Path: broad}); err == nil {
		t.Fatal("broad trace permissions were accepted")
	}
}

func TestStoreRejectsTracePathSwapAfterInitialization(t *testing.T) {
	t.Run("file symlink", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "state", "trace.jsonl")
		store, err := NewStore(StoreOptions{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := store.Record(Event{Stage: "task.started"}); err == nil {
			t.Fatal("trace append followed a symlink introduced after initialization")
		}
		if got := string(mustReadFile(t, target)); got != "unchanged" {
			t.Fatalf("symlink target changed: %q", got)
		}
	})

	t.Run("directory symlink", func(t *testing.T) {
		dir := t.TempDir()
		stateDir := filepath.Join(dir, "state")
		path := filepath.Join(stateDir, "trace.jsonl")
		store, err := NewStore(StoreOptions{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		originalDir := filepath.Join(dir, "state-original")
		if err := os.Rename(stateDir, originalDir); err != nil {
			t.Fatal(err)
		}
		redirectDir := filepath.Join(dir, "redirect")
		if err := os.Mkdir(redirectDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(redirectDir, stateDir); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := store.Record(Event{Stage: "task.started"}); err == nil {
			t.Fatal("trace append followed a directory symlink introduced after initialization")
		}
		if _, err := os.Stat(filepath.Join(redirectDir, "trace.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("redirected trace file exists: %v", err)
		}
	})
}

func TestProtocolTraceCorrelatesResponseAndDefaultsToMetadataOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "trace.jsonl")
	store, err := NewStore(StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	trace := NewTraceContext(TraceSeed{MessageID: "message-1"}).WithAgent("codex")
	outbound := []byte(`{"jsonrpc":"2.0","id":7,"method":"turn/start","params":{"threadId":"thread-1","input":[{"type":"text","text":"private prompt"}]}}`)
	if err := store.RecordProtocol(ProtocolRecord{
		Trace: trace, Direction: "outbound", AgentName: "codex", Protocol: "codex_app_server",
		WireEpoch: 3, Raw: outbound,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProtocol(ProtocolRecord{
		Direction: "inbound", AgentName: "codex", Protocol: "codex_app_server",
		WireEpoch: 3, Sequence: 9,
		Raw: []byte(`{"jsonrpc":"2.0","id":7,"result":{"turn":{"id":"turn-1","threadId":"thread-1"}}}`),
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.Query(context.Background(), Query{TraceID: trace.TraceID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("events=%#v", page.Events)
	}
	response := page.Events[1]
	if response.Method != "turn/start" || response.ThreadID != "thread-1" || response.TurnID != "turn-1" || response.Sequence != 9 {
		t.Fatalf("response=%#v", response)
	}
	if page.Events[0].Payload != "" || response.Payload != "" || strings.Contains(string(mustReadFile(t, path)), "private prompt") {
		t.Fatalf("protocol payload persisted by default: %#v", page.Events)
	}
}

func TestProtocolTraceCorrelatesTurnNotificationsWithoutRequestID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "trace.jsonl")
	store, err := NewStore(StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	trace := NewTraceContext(TraceSeed{MessageID: "message-1"}).WithAgent("codex")
	records := []ProtocolRecord{
		{Trace: trace, Direction: "outbound", AgentName: "codex", WireEpoch: 7,
			Raw: []byte(`{"jsonrpc":"2.0","id":11,"method":"turn/start","params":{"threadId":"thread-1"}}`)},
		{Direction: "inbound", AgentName: "codex", WireEpoch: 7,
			Raw: []byte(`{"jsonrpc":"2.0","id":11,"result":{"turn":{"id":"turn-1","threadId":"thread-1"}}}`)},
		{Direction: "inbound", AgentName: "codex", WireEpoch: 7, Sequence: 12,
			Raw: []byte(`{"jsonrpc":"2.0","method":"turn/plan/updated","params":{"threadId":"thread-1","turnId":"turn-1"}}`)},
	}
	for _, record := range records {
		if err := store.RecordProtocol(record); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Query(context.Background(), Query{TraceID: trace.TraceID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("events=%#v", page.Events)
	}
	notification := page.Events[2]
	if notification.Method != "turn/plan/updated" || notification.ThreadID != "thread-1" || notification.TurnID != "turn-1" || notification.Sequence != 12 {
		t.Fatalf("notification=%#v", notification)
	}
}

func TestProtocolTraceDoesNotInventTraceForUncorrelatedNotification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "trace.jsonl")
	store, err := NewStore(StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProtocol(ProtocolRecord{
		Direction: "inbound", AgentName: "codex", WireEpoch: 9,
		Raw: []byte(`{"jsonrpc":"2.0","method":"thread/status/changed","params":{"threadId":"unrelated"}}`),
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.Query(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].TraceID != "" || page.Events[0].ThreadID != "unrelated" {
		t.Fatalf("events=%#v", page.Events)
	}
}

func TestProtocolTraceRejectsOversizedCorrelationIdentifiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "trace.jsonl")
	store, err := NewStore(StoreOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", maxProtocolIdentifierBytes+1)
	outbound, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": oversized, "method": "turn/start",
		"params": map[string]any{"threadId": oversized},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProtocol(ProtocolRecord{
		Trace: NewTraceContext(TraceSeed{MessageID: "message-1"}), Direction: "outbound",
		AgentName: "codex", WireEpoch: 1, Raw: outbound,
	}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	requestCount := len(store.requestTraces)
	threadCount := len(store.threadTraces)
	store.mu.Unlock()
	if requestCount != 0 || threadCount != 0 {
		t.Fatalf("oversized identifiers entered correlation maps: requests=%d threads=%d", requestCount, threadCount)
	}
}

func TestProtocolPayloadOptInRedactsCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "trace.jsonl")
	store, err := NewStore(StoreOptions{Path: path, IncludeProtocolPayload: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"account/read","params":{"access_token":"secret-access","authorization":"Bearer secret-bearer","nested":{"refreshToken":"secret-refresh"}}}`)
	if err := store.RecordProtocol(ProtocolRecord{Direction: "outbound", AgentName: "codex", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	page, err := store.Query(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Payload == "" {
		t.Fatalf("events=%#v", page.Events)
	}
	payload := page.Events[0].Payload
	for _, secret := range []string{"secret-access", "secret-bearer", "secret-refresh"} {
		if strings.Contains(payload, secret) {
			t.Fatalf("payload leaked %q: %s", secret, payload)
		}
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
