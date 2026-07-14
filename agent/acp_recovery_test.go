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

// TestACPAgentResetSessionRestartsStoppedCodexRuntime 验证 Stop 后 /new 会重启 Codex runtime。

func TestACPAgentResetSessionRestartsStoppedCodexRuntime(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	a := NewACPAgent(ACPAgentConfig{
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestHelperCodexAppServer"},
		Cwd:       tempDir,
		StateFile: filepath.Join(tempDir, "acp-state.json"),
		Env:       map[string]string{testCodexAppServerEnv: "1"},
	})
	a.protocol = protocolCodexAppServer

	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	a.Stop()

	threadID, err := a.ResetSession(ctx, "user-1")
	if err != nil {
		t.Fatalf("ResetSession error: %v", err)
	}
	if threadID != testCodexThreadID {
		t.Fatalf("threadID=%q, want %s", threadID, testCodexThreadID)
	}
}

// TestACPAgentResetSessionRestartsAfterClosedCodexStdin 验证 started 状态残留但 stdin 已关闭时 /new 会自愈。

func TestACPAgentResetSessionRestartsAfterClosedCodexStdin(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	a := NewACPAgent(ACPAgentConfig{
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestHelperCodexAppServer"},
		Cwd:       tempDir,
		StateFile: filepath.Join(tempDir, "acp-state.json"),
		Env:       map[string]string{testCodexAppServerEnv: "1"},
	})
	a.protocol = protocolCodexAppServer
	_, closedWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe error: %v", err)
	}
	closedWriter.Close()
	a.stdin = closedWriter
	a.started = true

	threadID, err := a.ResetSession(ctx, "user-1")
	if err != nil {
		t.Fatalf("ResetSession error: %v", err)
	}
	if threadID != testCodexThreadID {
		t.Fatalf("threadID=%q, want %s", threadID, testCodexThreadID)
	}
}

func TestACPAgentLegacySessionNotFoundKeepsOriginalSession(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	writeACPStateFile(t, stateFile, acpPersistedState{
		Version:  acpPersistedStateVersion,
		Sessions: map[string]string{"user-1": "session-old"},
	})

	a := NewACPAgent(ACPAgentConfig{
		Command:   "claude-agent-acp",
		Cwd:       t.TempDir(),
		StateFile: stateFile,
	})
	a.mu.Lock()
	a.started = true
	a.mu.Unlock()
	if err := a.cacheAndValidateACPCapabilities(genericCapabilityPayload()); err != nil {
		t.Fatalf("cache capabilities: %v", err)
	}

	promptCalls := 0
	sessionStarts := 0
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "session/new":
			sessionStarts++
			return nil, fmt.Errorf("session/new must not be called")
		case "session/prompt":
			promptCalls++
			p := params.(promptParams)
			if p.SessionID != "session-old" {
				return nil, fmt.Errorf("prompt session=%q, want session-old", p.SessionID)
			}
			return nil, fmt.Errorf("agent error: Internal error；details=Session not found")
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	_, err := a.Chat(context.Background(), "user-1", "hello")
	if err == nil || !strings.Contains(err.Error(), "Session not found") {
		t.Fatalf("Chat() error=%v, want Session not found", err)
	}
	if promptCalls != 1 {
		t.Fatalf("promptCalls=%d, want 1", promptCalls)
	}
	if sessionStarts != 0 {
		t.Fatalf("session/new calls=%d, want 0", sessionStarts)
	}
	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Sessions["user-1"]; got != "session-old" {
		t.Fatalf("persisted session=%q, want session-old", got)
	}
}

func TestACPAgentCodexKeepsThreadOnEmptyResponse(t *testing.T) {
	ctx := context.Background()
	stateFile, workspace := filepath.Join(t.TempDir(), "acp-state.json"), t.TempDir()
	a := newCodexRecoveryAgent(workspace, stateFile)

	a.mu.Lock()
	a.threads["user-1"] = "old-thread"
	a.mu.Unlock()
	a.persistState()

	calls := map[string]int{}
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		calls[method]++
		switch method {
		case "thread/start":
			return nil, fmt.Errorf("thread/start must not be called")
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
			return nil, fmt.Errorf("unexpected thread id: %s", p.ThreadID)
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	_, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello"})
	if err == nil || !strings.Contains(err.Error(), "agent returned empty response") {
		t.Fatalf("chatCodexAppServer error=%v, want empty response", err)
	}
	if calls["thread/start"] != 0 {
		t.Fatalf("thread/start calls = %d, want 0", calls["thread/start"])
	}

	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["user-1"]; got != "old-thread" {
		t.Fatalf("persisted thread for user-1 = %q, want old-thread", got)
	}
}

// newCodexRecoveryAgent 创建使用独立状态文件的 Codex 恢复测试 Agent。
func newCodexRecoveryAgent(workspace string, stateFile string) *ACPAgent {
	return NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: workspace, StateFile: stateFile,
	})
}
