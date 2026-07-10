package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestHandleCodexErrorIgnoresRecoverableWebSocketStderr(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})
	a.stderr = &acpStderrWriter{prefix: "[test]"}
	_, _ = a.stderr.Write([]byte(`2026-05-21T09:02:00Z ERROR codex_api::endpoint::responses_websocket: failed to connect to websocket: HTTP error: 403 Forbidden, url: ws://192.168.201.10:4000/v1/responses`))

	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexError(json.RawMessage(`{}`))

	select {
	case evt := <-turnCh:
		t.Fatalf("recoverable websocket stderr should not fail turn, got %#v", evt)
	default:
	}
}

func TestFormatCodexErrorIgnoresRecoverableWebSocketMessage(t *testing.T) {
	got := formatCodexError(json.RawMessage(`{"message":"Falling back from WebSockets to HTTPS transport. unexpected status 403 Forbidden: Unknown error, url: ws://192.168.201.10:4000/v1/responses"}`))

	if got != "" {
		t.Fatalf("recoverable websocket fallback message should be ignored, got %q", got)
	}
}

func TestHandleCodexAutoApprovalReviewStartedEmitsProgress(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexAutoApprovalReviewStarted(json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","reviewId":"review-1"}`))

	select {
	case evt := <-turnCh:
		if evt.Kind != "progress" || !strings.Contains(evt.Text, "自动审批审核中") {
			t.Fatalf("event=%#v, want auto approval progress", evt)
		}
	default:
		t.Fatal("expected auto approval progress event")
	}
}

func TestHandleCodexGuardianWarningEmitsProgress(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexGuardianWarning(json.RawMessage(`{"threadId":"thread-1","message":"Automatic approval review approved (risk: medium)"}`))

	select {
	case evt := <-turnCh:
		if evt.Kind != "progress" || !containsAll(evt.Text, "自动审批", "通过") {
			t.Fatalf("event=%#v, want guardian approval progress", evt)
		}
	default:
		t.Fatal("expected guardian progress event")
	}
}

func TestHandleCodexTurnCompletedUsesNestedCompletedStatus(t *testing.T) {
	evt := handleCodexTurnEventForTest(t, `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}`)
	if evt.Kind != "completed" || evt.TurnID != "turn-1" {
		t.Fatalf("event=%#v, want completed turn-1", evt)
	}
}

func TestHandleCodexTurnCompletedReportsNestedInterruptedStatus(t *testing.T) {
	evt := handleCodexTurnEventForTest(t, `{"threadId":"thread-1","turn":{"id":"turn-1","status":"interrupted","items":[]}}`)
	if evt.Kind != "error" || !strings.Contains(evt.Text, "已中断") {
		t.Fatalf("event=%#v, want interrupted error", evt)
	}
}

func TestHandleCodexTurnCompletedReportsNestedFailure(t *testing.T) {
	evt := handleCodexTurnEventForTest(t, `{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"sandbox denied","codexErrorInfo":"SandboxError"}}}`)
	if evt.Kind != "error" || !containsAll(evt.Text, "sandbox denied", "SandboxError") {
		t.Fatalf("event=%#v, want nested failure detail", evt)
	}
}

func handleCodexTurnEventForTest(t *testing.T, params string) *codexTurnEvent {
	t.Helper()
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()
	a.handleCodexTurnEvent("turn/completed", json.RawMessage(params))
	select {
	case evt := <-turnCh:
		return evt
	default:
		t.Fatal("turn event was not dispatched")
		return nil
	}
}

func TestHandleCodexPlanUpdatedEmitsCurrentStepProgress(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexPlanUpdated(json.RawMessage(`{
		"threadId":"thread-1",
		"turnId":"turn-1",
		"plan":[
			{"step":"读取日志定位错误事件","status":"completed"},
			{"step":"修复实时状态渲染","status":"in_progress"},
			{"step":"运行回归测试","status":"pending"}
		]
	}`))

	select {
	case evt := <-turnCh:
		if evt.Kind != "progress" || evt.Text != "进展：修复实时状态渲染" {
			t.Fatalf("event=%#v, want current plan step progress", evt)
		}
	default:
		t.Fatal("expected plan progress event")
	}
}

func TestHandleCodexPlanUpdatedAcceptsCamelCaseStatus(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexPlanUpdated(json.RawMessage(`{"threadId":"thread-1","plan":[{"step":"对齐 Codex v2 协议","status":"inProgress"}]}`))

	select {
	case evt := <-turnCh:
		if evt.Text != "进展：对齐 Codex v2 协议" {
			t.Fatalf("event=%#v", evt)
		}
	default:
		t.Fatal("camelCase plan status did not emit progress")
	}
}

func TestHandleCodexItemStartedEmitsCommandProgress(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexItemStarted(json.RawMessage(`{"threadId":"thread-1","item":{"id":"cmd-1","type":"commandExecution","command":["go","test","./agent"],"cwd":"/workspace","status":"inProgress"}}`))

	select {
	case evt := <-turnCh:
		if evt.Progress == nil || evt.Progress.Action != "运行 go test ./agent" {
			t.Fatalf("event=%#v, want command progress", evt)
		}
	default:
		t.Fatal("commandExecution item did not emit progress")
	}
}

func TestHandleCodexItemStartedEmitsFileProgress(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexItemStarted(json.RawMessage(`{"threadId":"thread-1","item":{"id":"file-1","type":"fileChange","changes":[{"path":"agent/new.go","kind":{"type":"add"},"diff":"+package agent"}],"status":"inProgress"}}`))

	select {
	case evt := <-turnCh:
		if evt.Progress == nil || evt.Progress.Action != "新增 agent/new.go" || evt.Progress.FilePath != "agent/new.go" {
			t.Fatalf("event=%#v, want file progress", evt)
		}
	default:
		t.Fatal("fileChange item did not emit progress")
	}
}

func TestReadLoopHandlesFileChangePatchUpdated(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()
	raw := `{"jsonrpc":"2.0","method":"item/fileChange/patchUpdated","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"file-1","changes":[{"path":"agent/old.go","kind":{"type":"delete"},"diff":"-package agent"}]}}`
	a.mu.Lock()
	a.scanner = bufio.NewScanner(strings.NewReader(raw + "\n"))
	a.mu.Unlock()

	a.readLoop()

	select {
	case evt := <-turnCh:
		if evt.Progress == nil || evt.Progress.Action != "删除 agent/old.go" {
			t.Fatalf("event=%#v, want patchUpdated progress", evt)
		}
	default:
		t.Fatal("patchUpdated did not emit progress")
	}
}

func TestHandleCodexCommandProgressPrefersCommandLine(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexCommandProgress(json.RawMessage(`{
		"threadId":"thread-1",
		"command":["go","test","./agent"],
		"delta":"go test ./agent\nok github.com/fastclaw-ai/weclaw/agent 0.231s\n"
	}`))

	select {
	case evt := <-turnCh:
		if evt.Kind != "progress" || evt.Text != "运行 go test ./agent" {
			t.Fatalf("event=%#v, want readable command line", evt)
		}
	default:
		t.Fatal("expected raw command progress event")
	}
}

func TestHandleCodexCommandProgressFallsBackToLatestOutputLine(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexCommandProgress(json.RawMessage(`{
		"threadId":"thread-1",
		"delta":"go test ./agent\nok github.com/fastclaw-ai/weclaw/agent 0.231s\n"
	}`))

	select {
	case evt := <-turnCh:
		if evt.Kind != "progress" || evt.Text != "ok github.com/fastclaw-ai/weclaw/agent 0.231s" {
			t.Fatalf("event=%#v, want latest command output line", evt)
		}
	default:
		t.Fatal("expected raw command progress event")
	}
}

func TestHandleCodexFileProgressPrefersFilePath(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexFileProgress(json.RawMessage(`{
		"threadId":"thread-1",
		"filePath":"agent/codex_progress_events.go",
		"message":"*** Begin Patch\n*** Update File: agent/codex_progress_events.go\n+读取 Codex App 最新输出行\n"
	}`))

	select {
	case evt := <-turnCh:
		if evt.Kind != "progress" || evt.Text != "修改 agent/codex_progress_events.go" {
			t.Fatalf("event=%#v, want readable file line", evt)
		}
	default:
		t.Fatal("expected raw file progress event")
	}
}

func TestHandleCodexFileProgressExtractsPatchPath(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexFileProgress(json.RawMessage(`{
		"threadId":"thread-1",
		"message":"*** Begin Patch\n*** Update File: agent/codex_progress_events.go\n+读取 Codex App 最新输出行\n"
	}`))

	select {
	case evt := <-turnCh:
		if evt.Kind != "progress" || evt.Text != "修改 agent/codex_progress_events.go" {
			t.Fatalf("event=%#v, want readable patch file line", evt)
		}
	default:
		t.Fatal("expected raw file progress event")
	}
}

func TestHandleCodexProgressSkipsEmptyRawLine(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexCommandProgress(json.RawMessage(`{"threadId":"thread-1"}`))

	select {
	case evt := <-turnCh:
		t.Fatalf("empty raw progress should not emit synthetic status, got %#v", evt)
	default:
	}
}

func TestCodexTurnDiagnosticsAppendsRecentProgressToUnknownError(t *testing.T) {
	diagnostics := newCodexTurnDiagnostics(3)
	diagnostics.remember("进展：Codex 自动审批审核中。")
	diagnostics.remember("进展：Codex 已产生代码变更。")

	got := diagnostics.withError("Codex 返回未知错误")

	if !containsAll(got, "Codex 返回未知错误", "最近事件", "自动审批审核中", "已产生代码变更") {
		t.Fatalf("diagnostic error=%q, want recent turn events", got)
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
