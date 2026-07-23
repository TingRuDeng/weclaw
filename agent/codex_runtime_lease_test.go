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
	registry := newLegacyCodexRuntimeOwnerRegistry()
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
	registry := newLegacyCodexRuntimeOwnerRegistry()
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

func TestCodexRuntimeLeaseAllowsConcurrentDesktopTurn(t *testing.T) {
	registry := newLegacyCodexRuntimeOwnerRegistry()
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
	if binding.Runtime != CodexRuntimeDesktop || binding.ConflictReason != "" ||
		!binding.State.Active || binding.State.ActiveTurnID != "turn-remote" {
		t.Fatalf("binding=%#v，Desktop 的另一 turn 不应覆盖远程 lease", binding)
	}
	if err := lease.check(); err != nil {
		t.Fatalf("lease error=%v，Desktop 与 WeClaw 并存不应取消远程 turn", err)
	}
	lease.finish()
}

func TestCodexRuntimeLeaseAllowsConcurrentCompletedDesktopTurn(t *testing.T) {
	registry := newLegacyCodexRuntimeOwnerRegistry()
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

	binding, _ := registry.threadBinding("thread-1")
	if err := lease.check(); err != nil {
		t.Fatalf("lease error=%v，Desktop 完成另一 turn 不应取消远程 turn", err)
	}
	if !binding.State.Active || binding.State.ActiveTurnID != "turn-remote" || binding.State.LastTurnID != "turn-old" {
		t.Fatalf("binding=%#v，Desktop 终态不应覆盖远程 lease", binding)
	}
	lease.finish()
}

func TestCodexRuntimeLeaseAcceptsFastCompletedRemoteTurn(t *testing.T) {
	registry := newLegacyCodexRuntimeOwnerRegistry()
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
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
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
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
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

func TestCodexSharedHostSerializesDifferentFrontendRoutesPerThread(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	first := remoteCodexRuntimeRequest("thread-1", "feishu-window", 1)
	second := remoteCodexRuntimeRequest("thread-1", "wechat-window", 9)
	if _, err := registry.activateRuntime(first, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.beginTurn(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.activateRuntime(second, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatalf("second frontend binding should be observable during lease: %v", err)
	}
	if _, err := registry.beginTurn(second); !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("concurrent second writer error=%v, want writer busy", err)
	}
	lease.finish()

	if _, err := registry.activateRuntime(second, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	secondLease, err := registry.beginTurn(second)
	if err != nil {
		t.Fatalf("second frontend should write after lease release: %v", err)
	}
	secondLease.finish()
}

func TestCodexSharedHostLeaseRequiresAuthoritativeRuntime(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "feishu-window", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeUnknown, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.beginTurn(request); !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("beginTurn error=%v, want runtime unavailable", err)
	}
}

func TestCodexRuntimeRemoteIntentAcceptsUnleasedDesktopTurn(t *testing.T) {
	registry := newLegacyCodexRuntimeOwnerRegistry()
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}

	binding := registry.observeDesktopSnapshot("thread-1", 4, CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-local",
	})

	if binding.Runtime != CodexRuntimeDesktop || binding.ConflictReason != "" ||
		!binding.State.Active || binding.State.ActiveTurnID != "turn-local" {
		t.Fatalf("binding=%#v，空闲远程会话应接受 Desktop turn", binding)
	}
}

func TestCodexRuntimeRemoteIntentKeepsHandoffDesktopTurn(t *testing.T) {
	registry := newLegacyCodexRuntimeOwnerRegistry()
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

func TestCodexRuntimeReconcilesObservedHandoffTurnBeforeTerminal(t *testing.T) {
	registry := newLegacyCodexRuntimeOwnerRegistry()
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	active := CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-existing",
	}
	if _, err := registry.reconcileObservedTurn(request, active); err != nil {
		t.Fatalf("reconcile active: %v", err)
	}

	binding := registry.observeDesktopSnapshot("thread-1", 5, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-existing", LastTurnStatus: "completed",
	})

	if binding.Runtime != CodexRuntimeDesktop || binding.State.Active || binding.State.LastTurnID != "turn-existing" {
		t.Fatalf("binding=%#v，已接管 turn 的终态不应触发冲突", binding)
	}
}

func TestCodexRuntimeReconcileDoesNotClearExplicitConflict(t *testing.T) {
	registry := newLegacyCodexRuntimeOwnerRegistry()
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	state := CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-existing"}
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, state); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.markRuntimeConflict(request, "显式移交结果未确认"); err != nil {
		t.Fatal(err)
	}

	_, err := registry.reconcileObservedTurn(request, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-existing", LastTurnStatus: "completed",
	})

	if !errors.Is(err, ErrCodexRuntimeConflict) {
		t.Fatalf("error=%v，冲突态不能由终态同步自动解除", err)
	}
	binding, _ := registry.threadBinding("thread-1")
	if binding.Runtime != CodexRuntimeConflict {
		t.Fatalf("binding=%#v，冲突态被错误清除", binding)
	}
}

func TestCodexRuntimeReconcileTerminalAfterDesktopDisconnect(t *testing.T) {
	registry := newLegacyCodexRuntimeOwnerRegistry()
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	state := CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-existing"}
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, state); err != nil {
		t.Fatal(err)
	}
	registry.markDesktopDisconnected()

	binding, err := registry.reconcileObservedTurn(request, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-existing", LastTurnStatus: "completed",
	})

	if err != nil || binding.Runtime != CodexRuntimeUnknown || binding.State.Active || binding.State.LastTurnID != "turn-existing" {
		t.Fatalf("binding=%#v error=%v，断线后的同 turn 终态应只收敛 state", binding, err)
	}
}

func TestCodexRuntimeRemoteIntentAcceptsCompletedDesktopTurn(t *testing.T) {
	registry := newLegacyCodexRuntimeOwnerRegistry()
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	initial := CodexThreadState{ThreadID: "thread-1", LastTurnID: "turn-old"}
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, initial); err != nil {
		t.Fatal(err)
	}

	binding := registry.observeDesktopSnapshot("thread-1", 6, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-local", LastTurnStatus: "completed",
	})

	if binding.Runtime != CodexRuntimeDesktop || binding.ConflictReason != "" ||
		binding.State.LastTurnID != "turn-local" || binding.State.LastTurnStatus != "completed" {
		t.Fatalf("binding=%#v，空闲远程会话应接受 Desktop 终态", binding)
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
	if _, err := a.codexOwners.markRuntimeConflict(request, "显式移交结果未确认"); err != nil {
		t.Fatal(err)
	}

	runtime, _, inspectErr := a.probeCodexRuntime(context.Background(), request, codexRuntimeProbeOptions{})
	if runtime != CodexRuntimeConflict || !errors.Is(inspectErr, ErrCodexRuntimeConflict) {
		t.Fatalf("inspect runtime=%q err=%v", runtime, inspectErr)
	}
	runtime, _, handoffErr := a.probeCodexRuntime(context.Background(), request, codexRuntimeProbeOptions{allowConflictRecovery: true})
	if runtime != CodexRuntimeUnknown || handoffErr != nil {
		t.Fatalf("handoff runtime=%q err=%v", runtime, handoffErr)
	}
}

func TestCodexRuntimeConflictClearsOnConfirmedDesktopSnapshot(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := registry.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-old",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.markRuntimeConflict(request, "显式移交结果未确认"); err != nil {
		t.Fatal(err)
	}
	observed := CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-local",
	}
	binding := registry.observeDesktopSnapshot("thread-1", 8, observed)

	if binding.Runtime != CodexRuntimeDesktop || binding.ConflictReason != "" || binding.State.ActiveTurnID != "turn-local" {
		t.Fatalf("binding=%#v，已确认的 Desktop 快照应恢复可用 runtime", binding)
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

func TestRunCodexTurnRejectsActiveSharedHostWithoutLocalLease(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	turnStartCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"active","activeTurnId":"turn-existing"},"turns":[{"id":"turn-existing","status":"inProgress"}]}}`), nil
		case "turn/start":
			turnStartCalls++
			return nil, fmt.Errorf("turn/start must not be called while host thread is active")
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}

	_, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "不能重叠执行",
	})

	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("RunCodexTurn error=%v, want ErrCodexWriterBusy", err)
	}
	if turnStartCalls != 0 {
		t.Fatalf("turn/start calls=%d, want 0", turnStartCalls)
	}
	binding, ok := a.codexOwners.threadBinding(request.Ref.ThreadID)
	if !ok || !binding.State.Active || binding.State.ActiveTurnID != "turn-existing" {
		t.Fatalf("binding=%#v ok=%v, want authoritative active host state", binding, ok)
	}
}

func TestRunCodexTurnRetainsWriterLeaseAfterObservationDisconnect(t *testing.T) {
	a, request, _ := sharedHostObservationLossFixture(t)

	_, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "执行任务",
		OnTurnStarted: func(_ CodexThreadRef, _ string) error {
			a.failRuntimeWaitersUncertain("shared app-server connection lost")
			return nil
		},
	})
	var interrupted *CodexTurnInterruptedError
	if !errors.As(err, &interrupted) || interrupted.TurnID != "turn-1" {
		t.Fatalf("RunCodexTurn error=%v interrupted=%#v", err, interrupted)
	}
	if !a.codexOwners.hasWriterLease("thread-1") {
		t.Fatal("observation disconnect released writer lease before terminal confirmation")
	}
	if _, err := a.codexOwners.beginTurn(request); !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("second writer error=%v, want ErrCodexWriterBusy", err)
	}

	interrupted.ConfirmTerminal()
	if a.codexOwners.hasWriterLease("thread-1") {
		t.Fatal("confirmed terminal did not release retained writer lease")
	}
	binding, _ := a.codexOwners.threadBinding("thread-1")
	if binding.State.Active || binding.State.ActiveTurnID != "" {
		t.Fatalf("binding=%#v, want confirmed inactive state", binding)
	}
}

func TestInspectCodexRuntimeReconcilesUncertainWriterLease(t *testing.T) {
	a, request, markTerminal := sharedHostObservationLossFixture(t)

	_, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "执行任务",
		OnTurnStarted: func(_ CodexThreadRef, _ string) error {
			a.failRuntimeWaitersUncertain("shared app-server connection lost")
			return nil
		},
	})
	var interrupted *CodexTurnInterruptedError
	if !errors.As(err, &interrupted) {
		t.Fatalf("RunCodexTurn error=%v, want interrupted observation", err)
	}
	markTerminal()

	binding, err := a.InspectCodexRuntime(context.Background(), request)
	if err != nil {
		t.Fatalf("InspectCodexRuntime error=%v", err)
	}
	if a.codexOwners.hasWriterLease("thread-1") {
		t.Fatal("authoritative terminal snapshot did not release uncertain writer lease")
	}
	if binding.State.Active || binding.State.LastTurnID != "turn-1" || binding.State.LastTurnStatus != "completed" {
		t.Fatalf("binding=%#v, want reconciled terminal turn", binding)
	}
}

func TestUncertainLeaseWithoutTurnIDDoesNotReleaseForUnmatchedTerminal(t *testing.T) {
	registry := newCodexRuntimeOwnerRegistry(nil)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	initial := CodexThreadState{ThreadID: "thread-1", LastTurnID: "turn-before", LastTurnStatus: "completed"}
	if _, err := registry.activateRuntime(request, CodexRuntimeWeClaw, initial); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	lease.markUncertain()

	binding, retained, err := registry.reconcileUncertainSharedHostLease(request, CodexThreadState{
		ThreadID: "thread-1", LastTurnID: "turn-unmatched", LastTurnStatus: "completed",
	})

	if err != nil {
		t.Fatalf("reconcile error=%v", err)
	}
	if !retained || !registry.hasWriterLease(request.Ref.ThreadID) {
		t.Fatalf("retained=%v lease=%v, empty turn ID must remain fail-closed", retained, registry.hasWriterLease(request.Ref.ThreadID))
	}
	if !binding.State.Active {
		t.Fatalf("binding=%#v, ambiguous terminal must remain active until explicit confirmation", binding)
	}
}

func sharedHostObservationLossFixture(t *testing.T) (*ACPAgent, CodexRuntimeRequest, func()) {
	t.Helper()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	terminal := false
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			if terminal {
				return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[{"id":"turn-1","status":"completed"}]}}`), nil
			}
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}`), nil
		case "turn/start":
			return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}
	return a, request, func() { terminal = true }
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

func newLegacyCodexRuntimeOwnerRegistry() *codexRuntimeOwnerRegistry {
	return newCodexRuntimeOwnerRegistry(&codexDesktopOwnerProbeFake{})
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
