package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const (
	testCodexAppServerEnv = "WECLAW_TEST_CODEX_APP_SERVER"
	testACPExitEnv        = "WECLAW_TEST_ACP_EXIT"
	testCodexThreadID     = "thread-new"
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

func TestACPAgentCodexThreadStartIncludesEffort(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
		Model:   "gpt-5.4",
		Effort:  "high",
	})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/start" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p, ok := params.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected thread/start params type %T", params)
		}
		if p["model"] != "gpt-5.4" || p["effort"] != "high" {
			return nil, fmt.Errorf("model/effort params=%#v", p)
		}
		return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
	}

	if _, _, err := a.getOrCreateThread(ctx, "user-1"); err != nil {
		t.Fatalf("getOrCreateThread error: %v", err)
	}
}

func TestACPAgentCodexTurnStartIncludesEffort(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
		Model:   "gpt-5.4",
		Effort:  "high",
	})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			p, ok := params.(codexTurnStartParams)
			if !ok {
				return nil, fmt.Errorf("unexpected turn/start params type %T", params)
			}
			if p.Model != "gpt-5.4" || p.Effort != "high" {
				return nil, fmt.Errorf("model=%q effort=%q", p.Model, p.Effort)
			}
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			ch <- &codexTurnEvent{Delta: "ok"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	if _, err := a.chatCodexAppServer(ctx, "user-1", "hello", nil); err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
}

func TestACPAgentConversationCwdOverridesGlobalCwdForCodexThreadAndTurn(t *testing.T) {
	ctx := context.Background()
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("mkdir workspace A: %v", err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("mkdir workspace B: %v", err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     workspaceB,
	})
	a.SetConversationCwd("conversation-a", workspaceA)
	a.SetCwd(workspaceB)

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			p, ok := params.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("unexpected thread/start params type %T", params)
			}
			if p["cwd"] != workspaceA {
				return nil, fmt.Errorf("thread/start cwd=%q, want %q", p["cwd"], workspaceA)
			}
			return json.RawMessage(`{"thread":{"id":"thread-a"}}`), nil
		case "turn/start":
			p, ok := params.(codexTurnStartParams)
			if !ok {
				return nil, fmt.Errorf("unexpected turn/start params type %T", params)
			}
			if p.Cwd != workspaceA {
				return nil, fmt.Errorf("turn/start cwd=%q, want %q", p.Cwd, workspaceA)
			}
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			ch <- &codexTurnEvent{Delta: "ok"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	if _, err := a.chatCodexAppServer(ctx, "conversation-a", "hello", nil); err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
}

func TestACPAgentConversationCwdOverridesGlobalCwdForCodexResume(t *testing.T) {
	ctx := context.Background()
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("mkdir workspace A: %v", err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("mkdir workspace B: %v", err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     workspaceB,
	})
	a.SetConversationCwd("conversation-a", workspaceA)
	a.SetCwd(workspaceB)

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/resume" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p, ok := params.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected thread/resume params type %T", params)
		}
		if p["cwd"] != workspaceA {
			return nil, fmt.Errorf("thread/resume cwd=%q, want %q", p["cwd"], workspaceA)
		}
		return json.RawMessage(`{"thread":{"id":"thread-a"}}`), nil
	}

	if err := a.UseCodexThread(ctx, "conversation-a", "thread-a"); err != nil {
		t.Fatalf("UseCodexThread error: %v", err)
	}
}

func TestACPAgentCodexThreadControls(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       t.TempDir(),
		StateFile: stateFile,
	})
	resumed := ""
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "thread/resume" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		p := params.(map[string]interface{})
		resumed = p["threadId"].(string)
		return json.RawMessage(`{"thread":{"id":"thread-2"}}`), nil
	}

	if err := a.UseCodexThread(ctx, "conversation-1", "thread-2"); err != nil {
		t.Fatalf("UseCodexThread error: %v", err)
	}
	if resumed != "thread-2" {
		t.Fatalf("resumed thread=%q, want thread-2", resumed)
	}
	threadID, ok := a.CurrentCodexThread("conversation-1")
	if !ok || threadID != "thread-2" {
		t.Fatalf("CurrentCodexThread=(%q,%v), want thread-2 true", threadID, ok)
	}

	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["conversation-1"]; got != "thread-2" {
		t.Fatalf("persisted thread=%q, want thread-2", got)
	}

	a.ClearCodexThread("conversation-1")
	if _, ok := a.CurrentCodexThread("conversation-1"); ok {
		t.Fatal("ClearCodexThread should remove current thread")
	}
	persisted = readACPStateFile(t, stateFile)
	if _, ok := persisted.Threads["conversation-1"]; ok {
		t.Fatalf("cleared thread should not persist, got %q", persisted.Threads["conversation-1"])
	}
}
