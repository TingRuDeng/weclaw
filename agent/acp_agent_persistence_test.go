package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testCodexAppServerEnv = "WECLAW_TEST_CODEX_APP_SERVER"
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

func TestACPAgentReadLoopFailsPendingRequestsAndActiveTurnsOnEOF(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})
	pendingCh := make(chan *rpcResponse, 1)
	turnCh := make(chan *codexTurnEvent, 1)

	a.pendingMu.Lock()
	a.pending[7] = pendingCh
	a.pendingMu.Unlock()
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()
	a.mu.Lock()
	a.scanner = bufio.NewScanner(strings.NewReader(""))
	a.started = true
	a.mu.Unlock()

	a.readLoop()

	select {
	case resp := <-pendingCh:
		if resp.Error == nil || !strings.Contains(resp.Error.Message, "ACP runtime exited") {
			t.Fatalf("pending response=%#v, want ACP runtime exited error", resp)
		}
	default:
		t.Fatal("pending RPC did not receive runtime exit error")
	}
	select {
	case evt := <-turnCh:
		if evt.Kind != "error" || !strings.Contains(evt.Text, "ACP runtime exited") {
			t.Fatalf("turn event=%#v, want runtime exit error", evt)
		}
	default:
		t.Fatal("active turn did not receive runtime exit error")
	}

	a.pendingMu.Lock()
	_, pendingExists := a.pending[7]
	a.pendingMu.Unlock()
	if pendingExists {
		t.Fatal("pending RPC should be removed after runtime exit")
	}
}

func TestACPScannerReadsLargeCodexNotification(t *testing.T) {
	largePayload := strings.Repeat("x", 5*1024*1024)
	line := fmt.Sprintf(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps","status":"starting","detail":%q}}`, largePayload)
	scanner := newACPScanner(strings.NewReader(line + "\n"))

	if !scanner.Scan() {
		t.Fatalf("scanner failed to read large notification: %v", scanner.Err())
	}
	if scanner.Text() != line {
		t.Fatal("scanner returned unexpected large notification content")
	}
}

func TestACPAgentCallReturnsErrorWhenRuntimeStdinMissing(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})
	a.mu.Lock()
	a.started = false
	a.stdin = nil
	a.mu.Unlock()

	_, err := a.call(context.Background(), "turn/start", map[string]string{"threadId": "thread-1"})

	if err == nil || !strings.Contains(err.Error(), "ACP runtime is not running") {
		t.Fatalf("call error=%v, want runtime not running", err)
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

// TestHelperCodexAppServer 提供最小 Codex app-server，便于测试真实 stdin 生命周期。
func TestHelperCodexAppServer(t *testing.T) {
	if os.Getenv(testCodexAppServerEnv) != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     *int64 `json:"id,omitempty"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil || req.ID == nil {
			continue
		}
		result := map[string]interface{}{}
		if req.Method == "thread/start" {
			result = map[string]interface{}{"thread": map[string]string{"id": testCodexThreadID}}
		}
		_ = encoder.Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      *req.ID,
			"result":  result,
		})
	}
	os.Exit(0)
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

func TestACPAgentCodexAssemblerPrefersDeltaOverSnapshot(t *testing.T) {
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
			ch <- &codexTurnEvent{ItemID: "item-1", Text: "你好"}
			ch <- &codexTurnEvent{ItemID: "item-1", Delta: "你好"}
			ch <- &codexTurnEvent{ItemID: "item-1", Delta: "，世界"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	reply, err := a.chatCodexAppServer(ctx, "user-1", "hello", nil)
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "你好，世界" {
		t.Fatalf("reply=%q, want=%q", reply, "你好，世界")
	}
}

func TestACPAgentCodexAssemblerUsesSnapshotWhenNoDelta(t *testing.T) {
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
			ch <- &codexTurnEvent{ItemID: "item-1", Text: "完整 snapshot"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	reply, err := a.chatCodexAppServer(ctx, "user-1", "hello", nil)
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "完整 snapshot" {
		t.Fatalf("reply=%q, want=%q", reply, "完整 snapshot")
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

func TestFormatCodexErrorHandlesDeactivatedWorkspace(t *testing.T) {
	got := formatCodexError(json.RawMessage(`{"detail":{"code":"deactivated_workspace"}}`))

	if !containsAll(got, "Codex 工作区不可用", "deactivated_workspace") {
		t.Fatalf("formatCodexError=%q, want deactivated workspace detail", got)
	}
}

func TestFormatCodexErrorHandlesRawMessage(t *testing.T) {
	got := formatCodexError(json.RawMessage(`{"message":"HTTP error: 402 Payment Required","code":"deactivated_workspace"}`))

	if !containsAll(got, "402 Payment Required", "deactivated_workspace") {
		t.Fatalf("formatCodexError=%q, want raw message and code", got)
	}
}

func TestHandleCodexErrorUsesStderrWhenPayloadUnknown(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})
	a.stderr = &acpStderrWriter{prefix: "[test]"}
	_, _ = a.stderr.Write([]byte(`2026-04-27 ERROR codex_models_manager::manager: failed to refresh available models: unexpected status 402 Payment Required: {"detail":{"code":"deactivated_workspace"}}`))

	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexError(json.RawMessage(`{}`))

	select {
	case evt := <-turnCh:
		if evt.Kind != "error" {
			t.Fatalf("event kind=%q, want error", evt.Kind)
		}
		if !containsAll(evt.Text, "Codex 工作区不可用", "deactivated_workspace") {
			t.Fatalf("event text=%q, want stderr auth detail", evt.Text)
		}
	default:
		t.Fatal("expected stderr-enriched error event")
	}
}

func TestACPAgentInvalidatesCodexRuntimeOnAuthStateError(t *testing.T) {
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

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "turn/start":
			p := params.(codexTurnStartParams)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}
			ch <- &codexTurnEvent{Kind: "error", Text: "Codex 工作区不可用：(deactivated_workspace)"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	_, err := a.chatCodexAppServer(ctx, "user-1", "hello", nil)
	if err == nil {
		t.Fatal("chatCodexAppServer error = nil, want auth state error")
	}
	if !containsAll(err.Error(), "deactivated_workspace", "请重试") {
		t.Fatalf("error=%q, want retry hint with auth detail", err.Error())
	}
	persisted := readACPStateFile(t, stateFile)
	if _, ok := persisted.Threads["user-1"]; ok {
		t.Fatalf("auth state error should remove stale thread mapping, got %q", persisted.Threads["user-1"])
	}
}

func TestACPAgentKeepsRuntimeOnCodexUsageLimit(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       t.TempDir(),
		StateFile: stateFile,
	})
	a.started = true
	a.mu.Lock()
	a.threads["user-1"] = "old-thread"
	a.mu.Unlock()
	a.persistState()

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "turn/start":
			p := params.(codexTurnStartParams)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}
			ch <- &codexTurnEvent{Kind: "error", Text: "Codex 账号额度已用完：You've hit your usage limit. (usageLimitExceeded)"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	_, err := a.chatCodexAppServer(ctx, "user-1", "hello", nil)
	if err == nil {
		t.Fatal("chatCodexAppServer error = nil, want usage limit error")
	}
	if strings.Contains(err.Error(), "已刷新 Codex 进程") {
		t.Fatalf("usage limit should not refresh runtime, error=%q", err.Error())
	}
	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["user-1"]; got != "old-thread" {
		t.Fatalf("usage limit should keep thread mapping, got %q", got)
	}
}

func TestACPAgentRefreshesRuntimeOnNextTurnAfterUsageLimit(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       t.TempDir(),
		StateFile: stateFile,
	})
	a.started = true
	a.mu.Lock()
	a.threads["user-1"] = "old-thread"
	a.mu.Unlock()
	a.persistState()

	turnStarts := 0
	threadStarts := 0
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			threadStarts++
			return json.RawMessage(`{"thread":{"id":"new-thread"}}`), nil
		case "turn/start":
			turnStarts++
			p := params.(codexTurnStartParams)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}
			if turnStarts == 1 {
				if p.ThreadID != "old-thread" {
					t.Fatalf("first turn thread=%q, want old-thread", p.ThreadID)
				}
				ch <- &codexTurnEvent{Kind: "error", Text: "Codex 账号额度已用完：You've hit your usage limit. (usageLimitExceeded)"}
				return json.RawMessage(`{"ok":true}`), nil
			}
			if p.ThreadID != "new-thread" {
				t.Fatalf("second turn thread=%q, want new-thread", p.ThreadID)
			}
			ch <- &codexTurnEvent{Delta: "新账号回复"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	_, err := a.Chat(ctx, "user-1", "第一次请求")
	if err == nil {
		t.Fatal("first Chat error = nil, want usage limit")
	}
	if !containsAll(err.Error(), "usageLimitExceeded", "下一次请求") {
		t.Fatalf("usage limit error=%q, want next-request refresh hint", err.Error())
	}

	reply, err := a.Chat(ctx, "user-1", "切号后的请求")
	if err != nil {
		t.Fatalf("second Chat error: %v", err)
	}
	if reply != "新账号回复" {
		t.Fatalf("second reply=%q, want 新账号回复", reply)
	}
	if threadStarts != 1 {
		t.Fatalf("thread/start calls=%d, want 1", threadStarts)
	}
	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["user-1"]; got != "new-thread" {
		t.Fatalf("persisted thread=%q, want new-thread", got)
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

func TestHandleCodexDeltaDoesNotFallbackWithMultipleActiveTurns(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})
	firstTurnCh := make(chan *codexTurnEvent, 1)
	secondTurnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = firstTurnCh
	a.turnCh["thread-2"] = secondTurnCh
	a.notifyMu.Unlock()

	a.handleCodexDelta(json.RawMessage(`{"conversationId":"missing-thread","msg":{"delta":"wrong turn"}}`))

	select {
	case evt := <-firstTurnCh:
		t.Fatalf("unroutable delta should not reach thread-1, got %#v", evt)
	default:
	}
	select {
	case evt := <-secondTurnCh:
		t.Fatalf("unroutable delta should not reach thread-2, got %#v", evt)
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
