package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandoffCodexRuntimeDoesNotRestoreConversationClearedDuringRead(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	})
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			close(readStarted)
			<-releaseRead
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"notLoaded"}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	result := make(chan error, 1)
	go func() {
		_, err := a.HandoffCodexRuntime(context.Background(), request)
		result <- err
	}()

	<-readStarted
	a.ClearCodexThread(request.Ref.ConversationID)
	close(releaseRead)

	if err := <-result; !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("HandoffCodexRuntime error=%v, want stale binding conflict", err)
	}
	assertCodexConversationCleared(t, a, request.Ref.ConversationID)
}

func TestPrepareCodexRuntimeForWriteDoesNotRestoreConversationClearedDuringResume(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	binding, err := a.codexOwners.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{
		ThreadID: "thread-1", ThreadStatus: "notLoaded",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedCodexAppServerThreadBinding(a, request.Ref.ConversationID, request.Ref.ThreadID, true)
	resumeStarted := make(chan struct{})
	releaseResume := make(chan struct{})
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/resume":
			close(resumeStarted)
			<-releaseResume
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.prepareCodexRuntimeForWrite(context.Background(), request, binding)
		result <- err
	}()

	<-resumeStarted
	a.ClearCodexThread(request.Ref.ConversationID)
	close(releaseResume)

	if err := <-result; !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("prepareCodexRuntimeForWrite error=%v, want stale binding conflict", err)
	}
	assertCodexConversationCleared(t, a, request.Ref.ConversationID)
}

func TestResetSessionFailureClearsOwnerConversation(t *testing.T) {
	a := runtimeRecoveryTestAgent(t, CodexRuntimeUnknown)
	a.mu.Lock()
	a.threads["conversation-1"] = "thread-1"
	a.mu.Unlock()
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/start" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return nil, errors.New("start failed")
	}

	if _, err := a.ResetSession(context.Background(), "conversation-1"); err == nil {
		t.Fatal("ResetSession error=nil, want thread/start failure")
	}
	assertCodexConversationCleared(t, a, "conversation-1")
}

func TestUnsubscribeCodexThreadWaitsForAdmissionBeforeRuntimeDecision(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	a.markCodexThreadSubscribed(request.Ref.ThreadID)
	rpcCalled := make(chan struct{}, 1)
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		rpcCalled <- struct{}{}
		return json.RawMessage(`{"status":"unsubscribed"}`), nil
	}

	a.codexAdmissionMu.Lock()
	result := make(chan struct {
		attempted bool
		err       error
	}, 1)
	go func() {
		attempted, err := a.UnsubscribeCodexThread(context.Background(), request.Ref.ThreadID)
		result <- struct {
			attempted bool
			err       error
		}{attempted: attempted, err: err}
	}()
	select {
	case <-rpcCalled:
		a.codexAdmissionMu.Unlock()
		t.Fatal("unsubscribe issued RPC while runtime admission was locked")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		a.codexAdmissionMu.Unlock()
		t.Fatal(err)
	}
	a.codexAdmissionMu.Unlock()

	got := <-result
	if got.err != nil || got.attempted {
		t.Fatalf("UnsubscribeCodexThread attempted=%t err=%v, want Desktop skip", got.attempted, got.err)
	}
	select {
	case <-rpcCalled:
		t.Fatal("Desktop runtime must not send thread/unsubscribe")
	default:
	}
}

func TestInspectCodexRuntimeDoesNotRestoreConversationClearedDuringDesktopHistory(t *testing.T) {
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	probe := &codexDesktopOwnerProbeFake{
		socketExists:  true,
		processExists: true,
		loadFunc: func(context.Context, CodexThreadRef) error {
			close(loadStarted)
			<-releaseLoad
			return nil
		},
	}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: probe})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.mu.Lock()
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	a.mu.Unlock()
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeUnknown, CodexThreadState{
		ThreadID: request.Ref.ThreadID,
	}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.InspectCodexRuntime(context.Background(), request)
		result <- err
	}()

	<-loadStarted
	a.ClearCodexThread(request.Ref.ConversationID)
	close(releaseLoad)

	if err := <-result; !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("InspectCodexRuntime error=%v, want stale binding conflict", err)
	}
	assertCodexConversationCleared(t, a, request.Ref.ConversationID)
}

func TestInspectCodexRuntimeDoesNotReconcileUncertainLeaseAfterClear(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	seedCodexAppServerThreadBinding(a, request.Ref.ConversationID, request.Ref.ThreadID, false)
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{
		ThreadID: request.Ref.ThreadID, LastTurnID: "turn-before", LastTurnStatus: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := a.codexOwners.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish()
	if err := lease.accept("turn-stale"); err != nil {
		t.Fatal(err)
	}
	lease.markUncertain()

	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			close(readStarted)
			<-releaseRead
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[{"id":"turn-stale","status":"completed","items":[]}],"nextCursor":null}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.InspectCodexRuntime(context.Background(), request)
		result <- err
	}()

	<-readStarted
	a.ClearCodexThread(request.Ref.ConversationID)
	close(releaseRead)

	if err := <-result; !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("InspectCodexRuntime error=%v, want stale binding conflict", err)
	}
	if !a.codexOwners.hasWriterLease(request.Ref.ThreadID) {
		t.Fatal("stale thread/read released uncertain writer lease after Clear")
	}
	binding, ok := a.codexOwners.threadBinding(request.Ref.ThreadID)
	if !ok || !binding.State.Active || binding.State.ActiveTurnID != "turn-stale" {
		t.Fatalf("binding=%#v ok=%t, stale thread/read mutated uncertain lease state", binding, ok)
	}
	assertCodexConversationCleared(t, a, request.Ref.ConversationID)
}

func TestInspectCodexRuntimeDoesNotReconcileReplacementUncertainLease(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	seedCodexAppServerThreadBinding(a, request.Ref.ConversationID, request.Ref.ThreadID, false)
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{
		ThreadID: request.Ref.ThreadID, LastTurnID: "turn-before", LastTurnStatus: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	oldLease, err := a.codexOwners.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	defer oldLease.finish()
	if err := oldLease.accept("turn-old"); err != nil {
		t.Fatal(err)
	}
	oldLease.markUncertain()

	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			close(readStarted)
			<-releaseRead
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[{"id":"turn-stale","status":"completed","items":[]}],"nextCursor":null}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.InspectCodexRuntime(context.Background(), request)
		result <- err
	}()

	<-readStarted
	if _, retained, err := a.codexOwners.reconcileUncertainSharedHostLease(request, CodexThreadState{
		ThreadID: request.Ref.ThreadID, LastTurnID: "turn-observer", LastTurnStatus: "completed",
	}); err != nil || retained {
		t.Fatalf("resolve old lease retained=%t err=%v", retained, err)
	}
	newLease, err := a.codexOwners.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	defer newLease.finish()
	if err := newLease.accept("turn-new"); err != nil {
		t.Fatal(err)
	}
	newLease.markUncertain()
	close(releaseRead)

	if err := <-result; !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("InspectCodexRuntime error=%v, want replaced lease conflict", err)
	}
	if !a.codexOwners.hasWriterLease(request.Ref.ThreadID) {
		t.Fatal("stale thread/read released replacement uncertain writer lease")
	}
	binding, ok := a.codexOwners.threadBinding(request.Ref.ThreadID)
	if !ok || !binding.State.Active || binding.State.ActiveTurnID != "turn-new" {
		t.Fatalf("binding=%#v ok=%t, stale thread/read mutated replacement lease state", binding, ok)
	}
}

func TestResolveCodexRuntimeForInputDoesNotRestoreConversationClearedDuringDesktopHistory(t *testing.T) {
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	probe := &codexDesktopOwnerProbeFake{
		loadFunc: func(context.Context, CodexThreadRef) error {
			close(loadStarted)
			<-releaseLoad
			return nil
		},
	}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: probe})
	desktop := newCodexDesktopRuntime()
	desktop.state = newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	applyDesktopRefreshSnapshot(t, desktop.state, 1)
	a.desktopRuntime = desktop
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{
		ThreadID: request.Ref.ThreadID, ThreadStatus: "idle",
	}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.resolveCodexRuntimeForInputLocked(context.Background(), request)
		result <- err
	}()

	<-loadStarted
	a.ClearCodexThread(request.Ref.ConversationID)
	close(releaseLoad)

	if err := <-result; !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("resolveCodexRuntimeForInputLocked error=%v, want stale binding conflict", err)
	}
	assertCodexConversationCleared(t, a, request.Ref.ConversationID)
}

func TestReconcileCodexObservedTurnDoesNotRestoreClearedConversation(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "daemon",
		CodexDesktopBridge: true, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	state := CodexThreadState{
		ThreadID: request.Ref.ThreadID, LastTurnID: "turn-1", LastTurnStatus: "completed",
	}
	a.mu.Lock()
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	a.mu.Unlock()
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeWeClaw, state); err != nil {
		t.Fatal(err)
	}
	a.ClearCodexThread(request.Ref.ConversationID)

	if _, err := a.ReconcileCodexObservedTurn(context.Background(), request, state); !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("ReconcileCodexObservedTurn error=%v, want stale binding conflict", err)
	}
	assertCodexConversationCleared(t, a, request.Ref.ConversationID)
}

func TestMarkCodexRuntimeConflictDoesNotRestoreClearedConversation(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	seedCodexAppServerThreadBinding(a, request.Ref.ConversationID, request.Ref.ThreadID, false)
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeDesktop, CodexThreadState{
		ThreadID: request.Ref.ThreadID,
	}); err != nil {
		t.Fatal(err)
	}
	a.ClearCodexThread(request.Ref.ConversationID)

	if err := a.MarkCodexRuntimeConflict(context.Background(), request); !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("MarkCodexRuntimeConflict error=%v, want stale binding conflict", err)
	}
	assertCodexConversationCleared(t, a, request.Ref.ConversationID)
}

func TestArchiveCodexThreadRejectsUseStartedBeforeArchive(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: t.TempDir() + "/state.json",
	})
	seedCodexArchiveTestBindings(a)
	useReadStarted := make(chan struct{})
	releaseUseRead := make(chan struct{})
	var readCalls atomic.Int32
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			if readCalls.Add(1) == 1 {
				close(useReadStarted)
				<-releaseUseRead
				return json.RawMessage(`{"thread":{"id":"thread-archive","status":{"type":"notLoaded"}}}`), nil
			}
			return json.RawMessage(`{"thread":{"id":"thread-archive","status":{"type":"idle"}}}`), nil
		case "thread/archive":
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	useResult := make(chan error, 1)
	go func() {
		useResult <- a.UseCodexThread(context.Background(), "conversation-late", "thread-archive")
	}()

	<-useReadStarted
	if err := a.ArchiveCodexThread(context.Background(), "thread-archive"); err != nil {
		t.Fatalf("ArchiveCodexThread error=%v", err)
	}
	close(releaseUseRead)

	if err := <-useResult; !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("UseCodexThread error=%v, want archived lifecycle conflict", err)
	}
	assertCodexConversationCleared(t, a, "conversation-late")
	if _, ok := a.codexOwners.threadBinding("thread-archive"); ok {
		t.Fatal("stale UseCodexThread restored archived owner thread")
	}
}

func assertCodexConversationCleared(t *testing.T, a *ACPAgent, conversationID string) {
	t.Helper()
	if threadID, ok := a.CurrentCodexThread(conversationID); ok || threadID != "" {
		t.Fatalf("local binding=(%q,%t), want cleared", threadID, ok)
	}
	if binding, ok := a.codexOwners.currentConversationBinding(conversationID); ok {
		t.Fatalf("owner binding=%#v, want cleared", binding)
	}
}

func seedCodexAppServerThreadBinding(a *ACPAgent, conversationID string, threadID string, needsResume bool) {
	a.mu.Lock()
	a.threads[conversationID] = threadID
	if needsResume {
		a.resumeOnFirstUse[conversationID] = true
	} else {
		delete(a.resumeOnFirstUse, conversationID)
	}
	a.mu.Unlock()
	a.persistState()
}
