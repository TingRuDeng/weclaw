package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestCodexRuntimeLeaseAcceptsExpectedDesktopTurn(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}

	registry.observeDesktopSnapshot("thread-1", 2, CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	if err := lease.accept("turn-1"); err != nil {
		t.Fatalf("accept() error=%v", err)
	}
	binding, _ := registry.threadBinding("thread-1")
	if binding.Runtime != CodexRuntimeDesktop {
		t.Fatalf("runtime=%q，want desktop", binding.Runtime)
	}
	lease.finish()
}

func TestCodexRuntimeInspectReturnsSnapshotDuringWriterLease(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish()
	binding, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"})
	if err != nil || binding.Runtime != CodexRuntimeDesktop {
		t.Fatalf("binding=%#v err=%v", binding, err)
	}
}

func TestCodexRuntimeLeaseMarksUnexpectedDesktopTurnConflict(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.accept("turn-remote"); err != nil {
		t.Fatal(err)
	}

	registry.observeDesktopSnapshot("thread-1", 3, CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-local",
	})

	binding, _ := registry.threadBinding("thread-1")
	if binding.Runtime != CodexRuntimeConflict || binding.ConflictReason == "" {
		t.Fatalf("binding=%#v，want conflict", binding)
	}
	if err := lease.check(); !errors.Is(err, ErrCodexRuntimeConflict) {
		t.Fatalf("lease error=%v，want conflict", err)
	}
	lease.finish()
}

func TestCodexRuntimeLeaseMarksCompletedDesktopTurnConflict(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	initial := CodexThreadState{ThreadID: "thread-1", LastTurnID: "turn-old"}
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, initial); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.accept("turn-remote"); err != nil {
		t.Fatal(err)
	}

	registry.observeDesktopSnapshot("thread-1", 4, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-local", LastTurnStatus: "completed",
	})

	if err := lease.check(); !errors.Is(err, ErrCodexRuntimeConflict) {
		t.Fatalf("lease error=%v，want completed-turn conflict", err)
	}
	lease.finish()
}

func TestCodexRuntimeLeaseAcceptsFastCompletedRemoteTurn(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	initial := CodexThreadState{ThreadID: "thread-1", LastTurnID: "turn-old"}
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, initial); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	registry.observeDesktopSnapshot("thread-1", 5, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-remote", LastTurnStatus: "completed",
	})

	if err := lease.accept("turn-remote"); err != nil {
		t.Fatalf("accept fast completed turn: %v", err)
	}
	if err := lease.check(); err != nil {
		t.Fatalf("lease check: %v", err)
	}
	lease.finish()
}

func TestCodexRuntimeLeaseRejectsChangedControlRevision(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	request.Intent.Revision = 2

	_, err := registry.beginTurn(request)

	if !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("error=%v，want control changed", err)
	}
}

func TestCurrentCodexRuntimeRejectsControlChangeDuringWriterLease(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	lease, err := a.codexOwners.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish()

	next := remoteCodexRuntimeRequest("thread-1", "route-2", 2)
	_, err = a.CurrentCodexRuntime(next)

	if !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("error=%v，want control changed", err)
	}
	if probe.discoverCalls != 0 || probe.loadCalls != 0 {
		t.Fatalf("读取绑定时不应探测 Desktop: discover=%d load=%d", probe.discoverCalls, probe.loadCalls)
	}
}

func TestCodexRuntimeInspectAllowsOtherRouteButWriterLeaseRejectsIt(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-owner", 1)
	request.Ref.ConversationID = "conversation-viewer"

	binding, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"})
	if err != nil || binding.Control.RouteKey != "route-owner" {
		t.Fatalf("binding=%#v err=%v", binding, err)
	}
	if _, err := registry.beginTurn(request); !errors.Is(err, ErrCodexControlRequired) {
		t.Fatalf("beginTurn error=%v，want control required", err)
	}
}

func TestCodexRuntimeLeaseRejectsChangedGeneration(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	registry.mu.Lock()
	binding := registry.threads["thread-1"]
	binding.RuntimeGeneration++
	registry.threads["thread-1"] = binding
	registry.mu.Unlock()

	if err := lease.check(); !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("lease error=%v，want runtime unavailable", err)
	}
	lease.finish()
}

func TestCodexRuntimeRemoteIntentConflictsWithUnleasedDesktopTurn(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}

	binding := registry.observeDesktopSnapshot("thread-1", 4, CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-local",
	})

	if binding.Runtime != CodexRuntimeConflict {
		t.Fatalf("binding=%#v，want conflict", binding)
	}
}

func TestCodexRuntimeRemoteIntentKeepsHandoffDesktopTurn(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	state := CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-existing"}
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, state); err != nil {
		t.Fatal(err)
	}
	binding := registry.observeDesktopSnapshot("thread-1", 4, state)
	if binding.Runtime != CodexRuntimeDesktop || !binding.State.Active {
		t.Fatalf("binding=%#v，期望继续观察移交前任务", binding)
	}
}

func TestCodexRuntimeRemoteIntentRejectsCompletedDesktopTurn(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	initial := CodexThreadState{ThreadID: "thread-1", LastTurnID: "turn-old"}
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, initial); err != nil {
		t.Fatal(err)
	}

	binding := registry.observeDesktopSnapshot("thread-1", 6, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-local", LastTurnStatus: "completed",
	})

	if binding.Runtime != CodexRuntimeConflict {
		t.Fatalf("binding=%#v，want completed-turn conflict", binding)
	}
}

func TestCodexRuntimeConflictRequiresExplicitHandoffToRecover(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	initial := CodexThreadState{ThreadID: "thread-1", LastTurnID: "turn-old"}
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeDesktop, initial); err != nil {
		t.Fatal(err)
	}
	a.codexOwners.observeDesktopSnapshot("thread-1", 7, CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-local",
	})

	runtime, _, inspectErr := a.probeCodexRuntime(context.Background(), request, codexRuntimeProbeOptions{})
	if runtime != CodexRuntimeConflict || !errors.Is(inspectErr, ErrCodexRuntimeConflict) {
		t.Fatalf("inspect runtime=%q err=%v", runtime, inspectErr)
	}
	runtime, _, handoffErr := a.probeCodexRuntime(context.Background(), request, codexRuntimeProbeOptions{allowConflictRecovery: true})
	if runtime != CodexRuntimeUnknown || handoffErr != nil {
		t.Fatalf("handoff runtime=%q err=%v", runtime, handoffErr)
	}
}

func TestCodexRuntimeConflictSurvivesRepeatedDesktopSnapshot(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-old",
	}); err != nil {
		t.Fatal(err)
	}
	conflicting := CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-local",
	}
	registry.observeDesktopSnapshot("thread-1", 7, conflicting)

	binding := registry.observeDesktopSnapshot("thread-1", 8, conflicting)

	if binding.Runtime != CodexRuntimeConflict || binding.ConflictReason == "" {
		t.Fatalf("binding=%#v，重复快照不应清除冲突态", binding)
	}
}

func TestCodexRuntimeRemoteRouteHandoffReusesIdleWeClawRuntime(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	current := remoteCodexRuntimeRequest("thread-1", "route-old", 1)
	state := CodexThreadState{ThreadID: "thread-1", LastTurnID: "turn-old"}
	if _, err := a.codexOwners.activateRuntime(current, CodexRuntimeWeClaw, state); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	a.restartCodexAppServerCall = func(context.Context) error {
		restarts++
		return nil
	}
	next := remoteCodexRuntimeRequest("thread-1", "route-new", 2)

	binding, err := a.HandoffCodexRuntime(context.Background(), next)

	if err != nil || binding.Runtime != CodexRuntimeWeClaw || binding.Control.RouteKey != "route-new" {
		t.Fatalf("binding=%#v err=%v", binding, err)
	}
	if restarts != 0 {
		t.Fatalf("空闲 WeClaw runtime 不应重启，restarts=%d", restarts)
	}
}

func TestRunCodexTurnUsesValidatedWeClawRuntime(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	req := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	a.threads[req.Ref.ConversationID] = req.Ref.ThreadID
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "turn/start" {
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
		turn := params.(codexTurnStartParams)
		a.notifyMu.Lock()
		ch := a.turnCh[turn.ThreadID]
		a.notifyMu.Unlock()
		ch <- &codexTurnEvent{Delta: "受控执行成功"}
		ch <- &codexTurnEvent{Kind: "completed", TurnID: "turn-1"}
		return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
	}

	reply, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: req, Message: "继续任务",
	})

	if err != nil || reply != "受控执行成功" {
		t.Fatalf("reply=%q error=%v", reply, err)
	}
	if a.codexOwners.hasWriterLease("thread-1") {
		t.Fatal("turn 结束后 writer lease 未释放")
	}
	if probe.discoverCalls != 0 || probe.loadCalls != 0 {
		t.Fatalf("普通 turn 不应重新探测 Desktop: discover=%d load=%d", probe.discoverCalls, probe.loadCalls)
	}
}

func TestCodexRuntimeRequestValidationRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		req  CodexRuntimeRequest
		want error
	}{
		{name: "缺少 thread", req: CodexRuntimeRequest{}},
		{
			name: "远程路由缺失",
			req: CodexRuntimeRequest{
				Ref:    CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
				Intent: CodexControlIntent{Owner: CodexControlRemote},
			},
			want: ErrCodexControlRequired,
		},
		{
			name: "控制方无效",
			req: CodexRuntimeRequest{
				Ref:    CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
				Intent: CodexControlIntent{Owner: CodexControlOwner("invalid")},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRemoteCodexRequest(test.req)
			if err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v，want=%v", err, test.want)
			}
		})
	}
}

func TestHandoffCodexRuntimeUnclaimsWithoutDesktopProbe(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	req := CodexRuntimeRequest{
		Ref:    CodexThreadRef{ConversationID: "conversation-1", ThreadID: "thread-1"},
		Intent: CodexControlIntent{Owner: CodexControlUnclaimed, Revision: 2},
	}

	binding, err := a.HandoffCodexRuntime(context.Background(), req)

	if err != nil || binding.Runtime != CodexRuntimeUnknown || binding.Control.Owner != CodexControlUnclaimed {
		t.Fatalf("binding=%#v error=%v", binding, err)
	}
	if probe.discoverCalls != 0 || probe.loadCalls != 0 {
		t.Fatalf("未认领不应探测 Desktop: discover=%d load=%d", probe.discoverCalls, probe.loadCalls)
	}
}

func TestHandoffCodexRuntimeDesktopClearsExplicitConflict(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{socketExists: true, processExists: true}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	remote := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(remote, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	a.codexOwners.observeDesktopSnapshot("thread-1", 2, CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-local",
	})
	desktop := CodexRuntimeRequest{
		Ref: remote.Ref, Intent: CodexControlIntent{Owner: CodexControlDesktop, Revision: 2},
	}

	binding, err := a.HandoffCodexRuntime(context.Background(), desktop)

	if err != nil || binding.Runtime != CodexRuntimeDesktop || binding.ConflictReason != "" {
		t.Fatalf("binding=%#v error=%v", binding, err)
	}
}

func remoteCodexRuntimeRequest(threadID string, routeKey string, revision uint64) CodexRuntimeRequest {
	conversationID := "conversation-" + routeKey
	return CodexRuntimeRequest{
		Ref: CodexThreadRef{ConversationID: conversationID, ThreadID: threadID},
		Intent: CodexControlIntent{
			Owner: CodexControlRemote, RouteKey: routeKey,
			ConversationID: conversationID, Revision: revision,
		},
	}
}
