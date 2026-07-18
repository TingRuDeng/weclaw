package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testACPDelayedInitEnv = "WECLAW_TEST_ACP_DELAYED_INIT"

func TestACPAgentStartReturnsErrorWhenSubprocessExitsDuringInitialize(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperACPStartupExit"},
		Env:     map[string]string{testACPExitEnv: "1"},
	})
	a.protocol = protocolCodexAppServer

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("Start panic=%v, want startup error", recovered)
		}
	}()

	err := a.Start(ctx)
	if err == nil {
		t.Fatal("Start error = nil, want subprocess exit error")
	}
	if !strings.Contains(err.Error(), "agent startup failed") {
		t.Fatalf("Start error=%v, want agent startup failed", err)
	}
}

func TestACPAgentConcurrentStartWaitsForInitialize(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperACPDelayedInitialize"},
		Env:     map[string]string{testACPDelayedInitEnv: "1"},
	})
	a.protocol = protocolCodexAppServer
	t.Cleanup(a.Stop)

	firstDone := make(chan error, 1)
	go func() { firstDone <- a.Start(context.Background()) }()
	waitForACPProcessStarted(t, a)

	secondDone := make(chan error, 1)
	go func() { secondDone <- a.Start(context.Background()) }()
	select {
	case err := <-secondDone:
		t.Fatalf("second Start returned before initialize completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s Start error=%v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s Start did not finish", name)
		}
	}
}

func waitForACPProcessStarted(t *testing.T, a *ACPAgent) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		started := a.started
		a.mu.Unlock()
		if started {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("ACP subprocess did not start")
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

func TestACPAgentDispatchDuplicateResponseDoesNotBlock(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	id := int64(7)
	responseCh := make(chan *rpcResponse, 1)
	a.pending[id] = responseCh

	first := &rpcResponse{ID: &id, Result: json.RawMessage(`{"value":"first"}`)}
	duplicate := &rpcResponse{ID: &id, Result: json.RawMessage(`{"value":"duplicate"}`)}
	a.dispatchACPResponse(first)

	done := make(chan struct{})
	go func() {
		a.dispatchACPResponse(duplicate)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("duplicate RPC response blocked the ACP read loop")
	}

	if got := <-responseCh; got != first {
		t.Fatalf("pending response=%#v, want first response", got)
	}
}

func TestACPAgentRuntimeFailureEvictsQueuedStartedEvent(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	turnCh <- &codexTurnEvent{Kind: "started", TurnID: "turn-1"}
	a.turnCh["thread-1"] = turnCh

	a.failActiveTurns("runtime exited")

	event := <-turnCh
	if event.Kind != "error" || event.Text != "runtime exited" {
		t.Fatalf("turn event=%#v, want runtime error terminal", event)
	}
}

func TestACPAgentStopFailsPendingRequestsAndActiveTurns(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	pendingCh := make(chan *rpcResponse, 1)
	turnCh := make(chan *codexTurnEvent, 1)
	a.pending[9] = pendingCh
	a.turnCh["thread-1"] = turnCh
	a.started = true
	a.stdin = nopWriteCloser{Buffer: &bytes.Buffer{}}

	a.Stop()

	select {
	case resp := <-pendingCh:
		if resp.Error == nil || !strings.Contains(resp.Error.Message, "stopped") {
			t.Fatalf("pending response=%#v", resp)
		}
	default:
		t.Fatal("pending RPC was not failed by Stop")
	}
	select {
	case evt := <-turnCh:
		if evt.Kind != "error" || !strings.Contains(evt.Text, "stopped") {
			t.Fatalf("turn event=%#v", evt)
		}
	default:
		t.Fatal("active turn was not failed by Stop")
	}
}

func TestConfigureACPProcessSetsProcessGroupAndGracefulCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no process group on windows")
	}
	cmd := exec.Command("true")
	configureACPProcess(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("expected Setpgid to be enabled for ACP process")
	}
	if cmd.Cancel == nil {
		t.Fatal("expected graceful Cancel to be set")
	}
	if cmd.WaitDelay != acpKillGrace {
		t.Fatalf("expected WaitDelay=%s, got %s", acpKillGrace, cmd.WaitDelay)
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

func TestFormatRPCErrorMessageUsesStructuredData(t *testing.T) {
	err := &rpcError{
		Code:    -32000,
		Message: "OpenCode prompt failed",
		Data:    json.RawMessage(`{"reason":"missing API key","provider":"opencode"}`),
	}

	got := formatRPCErrorMessage(err, nil)

	if !containsAll(got, "OpenCode prompt failed", "missing API key", "opencode") {
		t.Fatalf("formatted error=%q, want message and structured data", got)
	}
}

func TestFormatRPCErrorMessageIgnoresUselessStderrBrace(t *testing.T) {
	err := &rpcError{Code: -32000, Message: "OpenCode prompt failed"}
	stderr := &acpStderrWriter{prefix: "[test]"}
	_, _ = stderr.Write([]byte("}\n"))

	got := formatRPCErrorMessage(err, stderr)

	if got != "OpenCode prompt failed" {
		t.Fatalf("formatted error=%q, want stderr brace ignored", got)
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

// TestHelperACPStartupExit 模拟 Codex optional dependency 缺失时子进程启动后立即退出。

func TestHelperACPStartupExit(t *testing.T) {
	if os.Getenv(testACPExitEnv) != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "Error: Missing optional dependency @openai/codex-darwin-x64")
	os.Exit(1)
}

// TestHelperACPDelayedInitialize 模拟进程已启动但 initialize 尚未完成的窗口。
func TestHelperACPDelayedInitialize(t *testing.T) {
	if os.Getenv(testACPDelayedInitEnv) != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(2)
	}
	var request rpcRequest
	if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
		os.Exit(3)
	}
	time.Sleep(200 * time.Millisecond)
	response := rpcResponse{JSONRPC: "2.0", ID: &request.ID, Result: json.RawMessage(`{}`)}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(4)
	}
	for scanner.Scan() {
	}
	os.Exit(0)
}
