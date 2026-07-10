package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

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
