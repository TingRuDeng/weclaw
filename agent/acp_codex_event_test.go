package agent

import (
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

func TestACPAgentConsumesCodexThreadSettingsUpdated(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	params := json.RawMessage(`{"threadId":"thread-1","threadSettings":{"model":"gpt-5.6-sol","effort":"max","serviceTier":"priority"}}`)
	if !a.dispatchCodexKnownNotification(rpcResponse{Method: "thread/settings/updated", Params: params}, `{}`) {
		t.Fatal("thread/settings/updated should be consumed as a known notification")
	}
	config, err := a.CodexThreadConfig(context.Background(), "conversation-1", "thread-1")
	if err != nil || config.Model != "gpt-5.6-sol" || config.Effort != "max" ||
		!config.ServiceTierKnown || config.ServiceTier != CodexServiceTierFast {
		t.Fatalf("CodexThreadConfig=(%#v,%v), want notification settings", config, err)
	}
	a.setCodexThreadConfigAt("thread-1", CodexThreadConfig{
		Model: "gpt-new", Effort: "high", ServiceTierKnown: true,
	}, 12)
	stale := json.RawMessage(`{"threadId":"thread-1","threadSettings":{"model":"gpt-old","effort":"low","serviceTier":"priority"}}`)
	a.dispatchCodexKnownNotification(rpcResponse{Method: "thread/settings/updated", Params: stale, Sequence: 11}, `{}`)
	config, err = a.CodexThreadConfig(context.Background(), "conversation-1", "thread-1")
	if err != nil || config.Model != "gpt-new" || config.Effort != "high" ||
		!config.ServiceTierKnown || config.ServiceTier != "" {
		t.Fatalf("stale notification overwrote config: (%#v,%v)", config, err)
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

func TestFormatCodexErrorRedactsCredentials(t *testing.T) {
	secret := "super-secret-value"
	got := formatCodexError(json.RawMessage(`{"message":"request failed api_key=` + secret + `"}`))

	if strings.Contains(got, secret) || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("formatCodexError=%q, want sanitized credentials", got)
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

func TestHandleCodexTurnCompletedUsesNestedCompletedStatus(t *testing.T) {
	evt := handleCodexTurnEventForTest(t, `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}`)
	if evt.Kind != "completed" || evt.TurnID != "turn-1" {
		t.Fatalf("event=%#v, want completed turn-1", evt)
	}
}

func TestHandleCodexItemCompletedAcceptsOfficialAgentMessageText(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	a.handleCodexItemCompletedAt(json.RawMessage(`{"threadId":"thread-1","item":{"id":"message-1","type":"agentMessage","text":"Codex 原文","status":"completed"}}`), 19)

	select {
	case event := <-turnCh:
		if event.Kind != "item_completed" || event.ItemID != "message-1" || event.Text != "Codex 原文" || event.Sequence != 19 {
			t.Fatalf("event=%#v", event)
		}
	default:
		t.Fatal("official agentMessage.text was not dispatched")
	}
}

func TestCodexSyntheticItemsDoNotEmitUserProgress(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 4)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	for _, params := range []string{
		`{"threadId":"thread-1","item":{"id":"cmd-1","type":"commandExecution","command":["go","test","./..."],"status":"inProgress"}}`,
		`{"threadId":"thread-1","item":{"id":"file-1","type":"fileChange","changes":[{"path":"secret.go"}],"status":"inProgress"}}`,
		`{"threadId":"thread-1","item":{"id":"tool-1","type":"mcpToolCall","server":"codegraph","tool":"explore","status":"inProgress"}}`,
	} {
		a.handleCodexItemStarted(json.RawMessage(params))
	}

	select {
	case event := <-turnCh:
		t.Fatalf("synthetic item leaked into user progress: %#v", event)
	default:
	}
}

func TestHandleCodexTurnCompletedReportsNestedInterruptedStatus(t *testing.T) {
	evt := handleCodexTurnEventForTest(t, `{"threadId":"thread-1","turn":{"id":"turn-1","status":"interrupted","items":[]}}`)
	if evt.Kind != "interrupted" || evt.TurnID != "turn-1" {
		t.Fatalf("event=%#v, want interrupted turn-1", evt)
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

func TestCodexInternalProgressNotificationsAreConsumedWithoutUserProgress(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 2)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	for _, method := range []string{
		"turn/plan/updated",
		"item/autoApprovalReview/started",
		"guardianWarning",
		"item/commandExecution/outputDelta",
		"item/fileChange/patchUpdated",
	} {
		message := rpcResponse{
			Method: method,
			Params: json.RawMessage(`{"threadId":"thread-1","message":"private payload"}`),
		}
		if !a.dispatchCodexNotification(message, "") {
			t.Fatalf("%s was not consumed", method)
		}
	}

	select {
	case event := <-turnCh:
		t.Fatalf("internal notification leaked into user progress: %#v", event)
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

func TestACPAgentKeepsCodexThreadOnAuthStateError(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()
	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
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
			ch <- &codexTurnEvent{Kind: "error", Text: "Codex 工作区不可用：(deactivated_workspace)"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	_, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello"})
	if err == nil {
		t.Fatal("chatCodexAppServer error = nil, want auth state error")
	}
	if !strings.Contains(err.Error(), "deactivated_workspace") {
		t.Fatalf("error=%q, want auth detail", err.Error())
	}
	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["user-1"]; got != "old-thread" {
		t.Fatalf("auth state error should keep thread mapping, got %q", got)
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

	_, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello"})
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

func TestACPAgentContinuesSameThreadAfterUsageLimit(t *testing.T) {
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
	request := remoteCodexRuntimeRequest("old-thread", "route-1", 1)
	request.Ref.ConversationID = "user-1"
	request.Intent.ConversationID = "user-1"
	a.desktopProbe = &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "old-thread"}); err != nil {
		t.Fatal(err)
	}

	turnStarts := 0
	threadStarts := 0
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			threadStarts++
			return nil, fmt.Errorf("thread/start must not be called")
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
				return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
			}
			if p.ThreadID != "old-thread" {
				t.Fatalf("second turn thread=%q, want old-thread", p.ThreadID)
			}
			ch <- &codexTurnEvent{Delta: "额度恢复后的回复"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"turn":{"id":"turn-2"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	_, err := a.RunCodexTurn(ctx, CodexTurnRequest{Runtime: request, Message: "第一次请求"})
	if err == nil {
		t.Fatal("first Chat error = nil, want usage limit")
	}
	if !strings.Contains(err.Error(), "usageLimitExceeded") {
		t.Fatalf("usage limit error=%q, want usage detail", err.Error())
	}

	reply, err := a.RunCodexTurn(ctx, CodexTurnRequest{Runtime: request, Message: "切号后的请求"})
	if err != nil {
		t.Fatalf("second Chat error: %v", err)
	}
	if reply != "额度恢复后的回复" {
		t.Fatalf("second reply=%q, want 额度恢复后的回复", reply)
	}
	if threadStarts != 0 {
		t.Fatalf("thread/start calls=%d, want 0", threadStarts)
	}
	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["user-1"]; got != "old-thread" {
		t.Fatalf("persisted thread=%q, want old-thread", got)
	}
}

// TestACPAgentKeepsCodexThreadWhenResumeReportsMissing 验证恢复失败不会自动新建 thread。
func TestACPAgentKeepsCodexThreadWhenResumeReportsMissing(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		StateFile: stateFile,
	})
	a.started = true
	a.mu.Lock()
	a.threads["user-1"] = "old-thread"
	a.resumeOnFirstUse["user-1"] = true
	a.mu.Unlock()
	a.persistState()
	request := remoteCodexRuntimeRequest("old-thread", "route-1", 1)
	request.Ref.ConversationID = "user-1"
	request.Intent.ConversationID = "user-1"
	a.desktopProbe = &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "old-thread"}); err != nil {
		t.Fatal(err)
	}
	threadStarts := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method == "thread/start" {
			threadStarts++
		}
		if method == "thread/resume" {
			return nil, fmt.Errorf("thread not found")
		}
		return nil, fmt.Errorf("unexpected rpc method: %s", method)
	}

	_, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{Runtime: request, Message: "hello"})
	if err == nil || !strings.Contains(err.Error(), "thread not found") {
		t.Fatalf("Chat error=%v, want thread not found", err)
	}
	if threadStarts != 0 {
		t.Fatalf("thread/start calls=%d, want 0", threadStarts)
	}
	persisted := readACPStateFile(t, stateFile)
	if got := persisted.Threads["user-1"]; got != "old-thread" {
		t.Fatalf("persisted thread=%q, want old-thread", got)
	}
}
