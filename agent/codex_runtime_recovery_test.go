package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestACPAgentResetSessionRebindsNewThreadRuntime(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeUnknown)
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/start" {
			return nil, errors.New("unexpected rpc method: " + method)
		}
		return json.RawMessage(`{"thread":{"id":"thread-new"}}`), nil
	}
	threadID, err := a.ResetSession(context.Background(), "conversation-1")
	if err != nil || threadID != "thread-new" {
		t.Fatalf("ResetSession()=(%q,%v), want thread-new", threadID, err)
	}
	binding, ok := a.runtimeBindingForThread("conversation-1", "thread-new")
	if !ok || binding.Runtime != CodexRuntimeWeClaw {
		t.Fatalf("binding=%#v ok=%v", binding, ok)
	}
}

func TestACPAgentResetSessionKeepsFreshThreadWritableBeforeFirstTurn(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeUnknown)
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-new"}}`), nil
		case "thread/read":
			request := params.(map[string]interface{})
			if request["threadId"] != "thread-new" || request["includeTurns"] != true {
				t.Fatalf("thread/read params=%#v", request)
			}
			return nil, errors.New("agent error: thread thread-new is not materialized yet; includeTurns is unavailable before first user message")
		default:
			return nil, errors.New("unexpected rpc method: " + method)
		}
	}

	threadID, err := a.ResetSession(context.Background(), "conversation-1")
	if err != nil || threadID != "thread-new" {
		t.Fatalf("ResetSession()=(%q,%v), want thread-new", threadID, err)
	}
	request := CodexRuntimeRequest{
		Ref: CodexThreadRef{ConversationID: "conversation-1", ThreadID: threadID},
		Intent: CodexControlIntent{
			Owner: CodexControlRemote, RouteKey: "route-1",
			ConversationID: "conversation-1", Revision: 1,
		},
	}
	binding, err := a.HandoffCodexRuntime(context.Background(), request)
	if err != nil || binding.Runtime != CodexRuntimeWeClaw {
		t.Fatalf("HandoffCodexRuntime() binding=%#v err=%v", binding, err)
	}
	state, err := a.ReadCodexThreadState(context.Background(), "conversation-1", threadID)
	if err != nil {
		t.Fatalf("ReadCodexThreadState() error=%v", err)
	}
	if state.ThreadID != threadID || state.Active || state.ActiveTurnID != "" {
		t.Fatalf("fresh thread state=%#v", state)
	}
	lease, err := a.codexOwners.beginTurn(request)
	if err != nil {
		t.Fatalf("fresh thread must accept first turn: %v", err)
	}
	lease.finish()
}

func TestACPAgentReadCodexThreadStatePreservesOtherReadErrors(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeUnknown)
	wantErr := errors.New("agent error: thread thread-new is not materialized yet")
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/read" {
			return nil, errors.New("unexpected rpc method: " + method)
		}
		return nil, wantErr
	}

	_, err := a.ReadCodexThreadState(context.Background(), "conversation-new", "thread-new")
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReadCodexThreadState() error=%v, want %v", err, wantErr)
	}
}

func TestACPAgentClearCodexThreadUnbindsConversation(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeUnknown)
	a.ClearCodexThread("conversation-1")
	if binding, ok := a.codexOwners.currentConversationBinding("conversation-1"); ok {
		t.Fatalf("binding=%#v，期望解除 conversation", binding)
	}
}

func TestACPAgentUseCodexThreadDoesNotResumeDesktopRuntime(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeDesktop)
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		t.Fatal("Desktop runtime 不应调用 app-server RPC")
		return nil, nil
	}
	if err := a.UseCodexThread(context.Background(), "conversation-2", "thread-1"); err != nil {
		t.Fatalf("UseCodexThread() error=%v", err)
	}
	binding, ok := a.runtimeBindingForThread("conversation-2", "thread-1")
	if !ok || binding.Runtime != CodexRuntimeDesktop {
		t.Fatalf("binding=%#v ok=%v", binding, ok)
	}
}

func TestACPAgentRecoveryDoesNotFailDesktopWatchers(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeDesktop)
	desktopCh := make(chan *codexTurnEvent, 1)
	appCh := make(chan *codexTurnEvent, 1)
	a.turnCh["thread-1"] = desktopCh
	a.turnCh["app-thread"] = appCh
	appCh <- &codexTurnEvent{Kind: "started", TurnID: "turn-1"}
	a.failAppServerActiveTurns("runtime restart")
	select {
	case event := <-desktopCh:
		t.Fatalf("desktop watcher received %#v", event)
	default:
	}
	select {
	case event := <-appCh:
		if event.Kind != "interrupted" {
			t.Fatalf("app event=%#v", event)
		}
	default:
		t.Fatal("app-server turn did not receive restart interruption")
	}
}

func TestACPAgentRestartWaitsForAppServerPermit(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeUnknown)
	permit, err := a.appServerGate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var restarts atomic.Int32
	a.restartCodexAppServerCall = func(context.Context) error {
		restarts.Add(1)
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- a.restartCodexAppServer(context.Background()) }()
	waitForCodexGateState(t, a.appServerGate, codexAppServerDraining)
	if restarts.Load() != 0 {
		t.Fatal("permit 释放前不应刷新 app-server")
	}
	permit.release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(codexGateTestTimeout):
		t.Fatal("app-server 刷新未完成")
	}
}

func TestACPAgentRestoredThreadResumeFailureIsReturned(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	a.threads["conversation-1"] = "thread-1"
	a.resumeOnFirstUse["conversation-1"] = true
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		return nil, errors.New("resume failed")
	}
	if _, err := a.requireThread(context.Background(), "conversation-1"); err == nil {
		t.Fatal("requireThread() error=nil")
	}
	if !a.resumeOnFirstUse["conversation-1"] {
		t.Fatal("resume failure cleared retry marker")
	}
}

func runtimeRecoveryTestAgent(t *testing.T, runtime CodexRuntimeHolder) *ACPAgent {
	t.Helper()
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	request := CodexRuntimeRequest{
		Ref:    CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
		Intent: CodexControlIntent{Owner: CodexControlUnclaimed},
	}
	if _, err := a.codexOwners.activateRuntime(request, runtime, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	return a
}
