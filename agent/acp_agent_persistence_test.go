package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestACPAgentPersistsAndRestoresCodexThread(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	if a.protocol != protocolCodexAppServer {
		t.Fatalf("protocol = %q, want %q", a.protocol, protocolCodexAppServer)
	}

	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	threadID, isNew, err := a.getOrCreateThread(ctx, "user-1")
	if err != nil {
		t.Fatalf("getOrCreateThread(create) error: %v", err)
	}
	if !isNew {
		t.Fatalf("isNew = %v, want true", isNew)
	}
	if threadID != "thread-1" {
		t.Fatalf("threadID = %q, want %q", threadID, "thread-1")
	}

	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["user-1"]; got != "thread-1" {
		t.Fatalf("persisted thread for user-1 = %q, want %q", got, "thread-1")
	}

	b := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	calls := map[string]int{}
	b.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		calls[method]++
		switch method {
		case "thread/resume":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method on restore: %s", method)
		}
	}

	restoredThreadID, restoredIsNew, err := b.getOrCreateThread(ctx, "user-1")
	if err != nil {
		t.Fatalf("getOrCreateThread(restore) error: %v", err)
	}
	if restoredIsNew {
		t.Fatalf("restored isNew = %v, want false", restoredIsNew)
	}
	if restoredThreadID != "thread-1" {
		t.Fatalf("restored threadID = %q, want %q", restoredThreadID, "thread-1")
	}
	if calls["thread/resume"] != 1 {
		t.Fatalf("thread/resume calls after first restore = %d, want 1", calls["thread/resume"])
	}

	_, _, err = b.getOrCreateThread(ctx, "user-1")
	if err != nil {
		t.Fatalf("getOrCreateThread(second restore call) error: %v", err)
	}
	if calls["thread/resume"] != 1 {
		t.Fatalf("thread/resume calls after second restore = %d, want 1", calls["thread/resume"])
	}
}

func TestACPAgentResetSessionPersistsDeletionOnCreateFailure(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	initial := acpPersistedState{
		Version: acpPersistedStateVersion,
		Threads: map[string]string{"user-1": "thread-old"},
	}
	writeACPStateFile(t, stateFile, initial)

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/start" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		return nil, fmt.Errorf("boom")
	}

	_, err := a.ResetSession(ctx, "user-1")
	if err == nil {
		t.Fatal("ResetSession error = nil, want non-nil")
	}

	persisted := readACPStateFile(t, stateFile)
	if _, ok := persisted.Threads["user-1"]; ok {
		t.Fatalf("expected user-1 thread mapping to be removed after reset failure, got %q", persisted.Threads["user-1"])
	}
}

func TestACPAgentCodexFallbackToFreshThreadOnEmptyResponse(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	a.mu.Lock()
	a.threads["user-1"] = "old-thread"
	a.mu.Unlock()
	a.persistState()

	calls := map[string]int{}
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		calls[method]++
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"new-thread"}}`), nil
		case "turn/start":
			p, ok := params.(codexTurnStartParams)
			if !ok {
				return nil, fmt.Errorf("unexpected turn/start params type %T", params)
			}

			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()

			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}

			if p.ThreadID == "old-thread" {
				ch <- &codexTurnEvent{Kind: "completed"}
				return json.RawMessage(`{"ok":true}`), nil
			}
			if p.ThreadID == "new-thread" {
				ch <- &codexTurnEvent{Delta: "fresh reply"}
				ch <- &codexTurnEvent{Kind: "completed"}
				return json.RawMessage(`{"ok":true}`), nil
			}
			return nil, fmt.Errorf("unexpected thread id: %s", p.ThreadID)
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	reply, err := a.chatCodexAppServer(ctx, "user-1", "hello", nil)
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "fresh reply" {
		t.Fatalf("reply = %q, want %q", reply, "fresh reply")
	}
	if calls["thread/start"] != 1 {
		t.Fatalf("thread/start calls = %d, want 1", calls["thread/start"])
	}

	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["user-1"]; got != "new-thread" {
		t.Fatalf("persisted thread for user-1 = %q, want %q", got, "new-thread")
	}
}

func TestACPAgentCodexProgressCallbackReceivesDelta(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			p := params.(codexTurnStartParams)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}
			ch <- &codexTurnEvent{Delta: "实时片段"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	var got []string
	reply, err := a.chatCodexAppServer(ctx, "user-1", "hello", func(delta string) {
		got = append(got, delta)
	})
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "实时片段" {
		t.Fatalf("reply=%q, want=%q", reply, "实时片段")
	}
	if len(got) != 1 || got[0] != "实时片段" {
		t.Fatalf("progress deltas=%v, want=[实时片段]", got)
	}
}

func TestACPAgentCodexErrorNotificationReachesActiveTurn(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})

	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexError(json.RawMessage(`{"error":{"message":"You've hit your usage limit. Try again later.","codexErrorInfo":"usageLimitExceeded"}}`))

	select {
	case evt := <-turnCh:
		if evt.Kind != "error" {
			t.Fatalf("event kind=%q, want error", evt.Kind)
		}
		if !containsAll(evt.Text, "You've hit your usage limit", "usageLimitExceeded") {
			t.Fatalf("event text did not include codex error details: %q", evt.Text)
		}
	default:
		t.Fatal("expected error event to be delivered to active turn")
	}
}

func TestDispatchToTurnChFallbackOnlyWhenSingleActiveTurn(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})

	singleTurnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = singleTurnCh
	a.notifyMu.Unlock()

	a.dispatchToTurnCh("", &codexTurnEvent{Delta: "single"})
	select {
	case evt := <-singleTurnCh:
		if evt.Delta != "single" {
			t.Fatalf("single active fallback event delta=%q, want single", evt.Delta)
		}
	default:
		t.Fatal("single active turn should receive fallback event")
	}

	secondTurnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-2"] = secondTurnCh
	a.notifyMu.Unlock()

	a.dispatchToTurnCh("", &codexTurnEvent{Delta: "multi"})
	select {
	case evt := <-singleTurnCh:
		t.Fatalf("multi active turn should not fallback to thread-1, got %#v", evt)
	default:
	}
	select {
	case evt := <-secondTurnCh:
		t.Fatalf("multi active turn should not fallback to thread-2, got %#v", evt)
	default:
	}
}

func TestCodexInitializeParamsDeclareExperimentalAPI(t *testing.T) {
	params := codexInitializeParams()

	clientInfo, ok := params["clientInfo"].(map[string]string)
	if !ok {
		t.Fatalf("clientInfo type=%T, want map[string]string", params["clientInfo"])
	}
	if clientInfo["name"] != "weclaw" {
		t.Fatalf("clientInfo name=%q, want weclaw", clientInfo["name"])
	}

	caps, ok := params["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatalf("capabilities type=%T, want map[string]interface{}", params["capabilities"])
	}
	if caps["experimentalApi"] != true {
		t.Fatalf("experimentalApi=%v, want true", caps["experimentalApi"])
	}
}

func TestACPAgentCodexRehydratePromptUsesLocalHistory(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})
	a.recordConversationExchange("user-1", "我们之前讨论到哪了？", "我们在讨论会话持久化与恢复。")

	a.mu.Lock()
	a.threads["user-1"] = "stale-thread"
	a.mu.Unlock()
	a.persistState()

	var retryPrompt string
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"fresh-thread"}}`), nil
		case "turn/start":
			p := params.(codexTurnStartParams)

			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}

			if p.ThreadID == "stale-thread" {
				ch <- &codexTurnEvent{Kind: "completed"}
				return json.RawMessage(`{"ok":true}`), nil
			}
			if p.ThreadID == "fresh-thread" {
				retryPrompt = p.Input[0].Text
				ch <- &codexTurnEvent{Delta: "ok"}
				ch <- &codexTurnEvent{Kind: "completed"}
				return json.RawMessage(`{"ok":true}`), nil
			}
			return nil, fmt.Errorf("unexpected thread id: %s", p.ThreadID)
		default:
			return nil, fmt.Errorf("unexpected method: %s", method)
		}
	}

	reply, err := a.chatCodexAppServer(ctx, "user-1", "继续刚才的话题", nil)
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "ok" {
		t.Fatalf("reply = %q, want %q", reply, "ok")
	}
	if retryPrompt == "" {
		t.Fatal("retryPrompt is empty")
	}
	if !containsAll(retryPrompt,
		"Context from the previous conversation",
		"我们之前讨论到哪了？",
		"会话持久化与恢复",
		"继续刚才的话题",
	) {
		t.Fatalf("retry prompt did not contain expected context.\nretryPrompt=%q", retryPrompt)
	}
}

func readACPStateFile(t *testing.T, path string) acpPersistedState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file %s: %v", path, err)
	}
	var state acpPersistedState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal state file %s: %v", path, err)
	}
	return state
}

func writeACPStateFile(t *testing.T, path string, state acpPersistedState) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write state file %s: %v", path, err)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
