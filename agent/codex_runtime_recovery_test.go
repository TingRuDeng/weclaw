package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestACPAgentRecoverCodexThreadRejectsLiveOwner(t *testing.T) {
	a := recoveryTestAgent(t, CodexOwnerDesktopLive)
	err := a.RecoverCodexThread(context.Background(), CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	})
	if !errors.Is(err, ErrCodexDesktopOwnershipUnknown) {
		t.Fatalf("RecoverCodexThread() error = %v", err)
	}
}

func TestACPAgentUseCodexThreadDoesNotResumeDesktopOwner(t *testing.T) {
	a := recoveryTestAgent(t, CodexOwnerDesktopLive)
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		t.Fatal("Desktop owner 不应调用 app-server RPC")
		return nil, nil
	}
	err := a.UseCodexThread(context.Background(), "conversation-2", "thread-1")
	if err != nil {
		t.Fatalf("UseCodexThread() error = %v", err)
	}
	binding, ok := a.CurrentCodexThreadBinding("conversation-2")
	if !ok || binding.Owner != CodexOwnerDesktopLive {
		t.Fatalf("binding = %#v, ok = %v", binding, ok)
	}
}

func TestACPAgentRecoverCodexThreadRejectsDisconnectedOwner(t *testing.T) {
	a := recoveryTestAgent(t, CodexOwnerDesktopDisconnected)
	err := a.RecoverCodexThread(context.Background(), CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	})
	if !errors.Is(err, ErrCodexDesktopDisconnected) {
		t.Fatalf("RecoverCodexThread() error = %v", err)
	}
}

func TestACPAgentRecoverCodexThreadRejectsActiveACPTurn(t *testing.T) {
	a := recoveryTestAgent(t, CodexOwnerPersistedOnly)
	a.turnCh["thread-1"] = make(chan *codexTurnEvent, 1)
	err := a.RecoverCodexThread(context.Background(), CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	})
	if err == nil {
		t.Fatal("RecoverCodexThread() error = nil")
	}
}

func TestACPAgentRecoverCodexThreadRestartsBeforeResume(t *testing.T) {
	a := recoveryTestAgent(t, CodexOwnerPersistedOnly)
	var order []string
	a.restartCodexAppServerCall = func(context.Context) error {
		order = append(order, "restart")
		return nil
	}
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		order = append(order, method)
		return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
	}
	err := a.RecoverCodexThread(context.Background(), CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	})
	if err != nil {
		t.Fatalf("RecoverCodexThread() error = %v", err)
	}
	if len(order) != 2 || order[0] != "restart" || order[1] != "thread/resume" {
		t.Fatalf("order = %#v", order)
	}
	if a.threads["conversation-1"] != "thread-1" {
		t.Fatalf("threads = %#v", a.threads)
	}
}

func TestACPAgentRecoversPersistedWeClawThreadAfterRestart(t *testing.T) {
	stateFile := t.TempDir() + "/state.json"
	source := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: stateFile,
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	binding := source.codexOwners.claimWeClawThread(
		"thread-1", CodexThreadState{ThreadID: "thread-1"},
	)
	ref := CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"}
	source.codexOwners.bindConversation(ref, binding)
	source.persistState()

	restored := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: stateFile,
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	var order []string
	restored.restartCodexAppServerCall = func(context.Context) error {
		order = append(order, "restart")
		return nil
	}
	restored.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		order = append(order, method)
		return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
	}
	if err := restored.RecoverCodexThread(context.Background(), ref); err != nil {
		t.Fatalf("RecoverCodexThread() error = %v", err)
	}
	if len(order) != 2 || order[0] != "restart" || order[1] != "thread/resume" {
		t.Fatalf("order = %#v", order)
	}
	if restored.threads["conversation-1"] != "thread-1" {
		t.Fatalf("threads = %#v", restored.threads)
	}
}

func TestACPAgentRecoveryDoesNotFailDesktopWatchers(t *testing.T) {
	a := recoveryTestAgent(t, CodexOwnerDesktopLive)
	desktopCh := make(chan *codexTurnEvent, 1)
	appCh := make(chan *codexTurnEvent, 1)
	a.turnCh["thread-1"] = desktopCh
	a.turnCh["app-thread"] = appCh
	a.failAppServerActiveTurns("runtime restart")
	select {
	case event := <-desktopCh:
		t.Fatalf("desktop watcher received %#v", event)
	default:
	}
	select {
	case event := <-appCh:
		if event.Kind != "error" {
			t.Fatalf("app event = %#v", event)
		}
	default:
		t.Fatal("app-server turn did not receive restart error")
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
	if _, _, err := a.getOrCreateThread(context.Background(), "conversation-1"); err == nil {
		t.Fatal("getOrCreateThread() error = nil")
	}
	if !a.resumeOnFirstUse["conversation-1"] {
		t.Fatal("resume failure cleared retry marker")
	}
}

func recoveryTestAgent(t *testing.T, owner CodexRuntimeOwner) *ACPAgent {
	t.Helper()
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	if owner == CodexOwnerDesktopLive {
		binding := a.codexOwners.observeDesktopSnapshot(
			"thread-1", 1, CodexThreadState{ThreadID: "thread-1"},
		)
		a.codexOwners.bindConversation(CodexThreadRef{
			ConversationID: "conversation-1", ThreadID: "thread-1",
		}, binding)
		return a
	}
	a.codexOwners.restoreBindings(map[string]CodexThreadBinding{
		"conversation-1": {
			Ref:   CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
			Owner: owner, ReleaseConfirmed: owner == CodexOwnerPersistedOnly,
		},
	})
	return a
}
