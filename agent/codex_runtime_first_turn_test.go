package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestRunCodexTurnReplacesMissingPendingFirstTurnEndToEnd(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}})
	a.restartCodexAppServerCall = func(context.Context) error { return nil }
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/resume":
			return nil, errors.New("agent error: no rollout found for thread id thread-old")
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-new"}}`), nil
		case "turn/start":
			turn := params.(codexTurnStartParams)
			if turn.ThreadID != "thread-new" {
				t.Fatalf("turn thread=%q", turn.ThreadID)
			}
			a.notifyMu.Lock()
			ch := a.turnCh[turn.ThreadID]
			a.notifyMu.Unlock()
			ch <- &codexTurnEvent{Delta: "补建后执行成功"}
			ch <- &codexTurnEvent{Kind: "completed", TurnID: "turn-new"}
			return json.RawMessage(`{"turn":{"id":"turn-new"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}
	request := remoteCodexRuntimeRequest("thread-old", "route-1", 1)
	request.PendingFirstTurn = true
	var replaced CodexThreadRef
	var started CodexThreadRef
	var refsMu sync.Mutex

	reply, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "第一条消息",
		OnThreadReplaced: func(_ CodexThreadRef, current CodexThreadRef) error {
			refsMu.Lock()
			defer refsMu.Unlock()
			replaced = current
			return nil
		},
		OnTurnStarted: func(thread CodexThreadRef, turnID string) error {
			if turnID != "turn-new" {
				t.Fatalf("turnID=%q", turnID)
			}
			refsMu.Lock()
			defer refsMu.Unlock()
			started = thread
			return nil
		},
	})
	if err != nil || reply != "补建后执行成功" {
		t.Fatalf("reply=%q error=%v", reply, err)
	}
	refsMu.Lock()
	replacedSnapshot, startedSnapshot := replaced, started
	refsMu.Unlock()
	if replacedSnapshot.ThreadID != "thread-new" || startedSnapshot.ThreadID != "thread-new" {
		t.Fatalf("replaced=%#v started=%#v", replacedSnapshot, startedSnapshot)
	}
	if binding, ok := a.codexOwners.threadBinding("thread-new"); !ok ||
		binding.Runtime != CodexRuntimeWeClaw || binding.Control != request.Intent {
		t.Fatalf("binding=%#v ok=%v", binding, ok)
	}
}

func TestReplaceMissingFirstTurnThreadCreatesWritableReplacement(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeUnknown)
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/start" {
			t.Fatalf("method=%q, want thread/start", method)
		}
		return json.RawMessage(`{"thread":{"id":"thread-new"}}`), nil
	}
	request := CodexTurnRequest{
		Runtime: remoteCodexRuntimeRequest("thread-old", "route-1", 4),
		Message: "第一条消息",
	}
	request.Runtime.PendingFirstTurn = true
	var previous, current CodexThreadRef
	request.OnThreadReplaced = func(oldRef CodexThreadRef, newRef CodexThreadRef) error {
		previous, current = oldRef, newRef
		return nil
	}

	replaced, binding, err := a.replaceMissingFirstTurnThread(
		context.Background(), request,
		errors.New("agent error: no rollout found for thread id thread-old"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if previous.ThreadID != "thread-old" || current.ThreadID != "thread-new" ||
		replaced.Runtime.Ref.ThreadID != "thread-new" {
		t.Fatalf("previous=%#v current=%#v request=%#v", previous, current, replaced.Runtime.Ref)
	}
	if binding.Runtime != CodexRuntimeWeClaw || binding.Control != request.Runtime.Intent {
		t.Fatalf("binding=%#v", binding)
	}
}

func TestReplaceMissingMaterializedThreadDoesNotCreateReplacement(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeUnknown)
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		t.Fatal("已有历史的 thread 不得自动补建")
		return nil, nil
	}
	wantErr := errors.New("agent error: no rollout found for thread id thread-old")
	request := CodexTurnRequest{
		Runtime:          remoteCodexRuntimeRequest("thread-old", "route-1", 4),
		OnThreadReplaced: func(CodexThreadRef, CodexThreadRef) error { return nil },
	}

	_, _, err := a.replaceMissingFirstTurnThread(context.Background(), request, wantErr)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want original missing error", err)
	}
}
