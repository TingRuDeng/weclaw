package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestUnsubscribeCodexThreadDoesNotRestartHost(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.markCodexThreadSubscribed("thread-old")
	a.stopManagedHostCall = func(context.Context, string) error {
		t.Fatal("ordinary unsubscribe must not stop the Host")
		return nil
	}
	a.startManagedHostCall = func(context.Context, string) error {
		t.Fatal("ordinary unsubscribe must not start the Host")
		return nil
	}
	called := 0
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		called++
		if method != "thread/unsubscribe" {
			t.Fatalf("method=%q, want thread/unsubscribe", method)
		}
		if got := params.(map[string]interface{})["threadId"]; got != "thread-old" {
			t.Fatalf("threadId=%v", got)
		}
		return json.RawMessage(`{"status":"unsubscribed"}`), nil
	}

	attempted, err := a.UnsubscribeCodexThread(context.Background(), "thread-old")

	if err != nil || !attempted || called != 1 {
		t.Fatalf("attempted=%v calls=%d err=%v", attempted, called, err)
	}
}

func TestUnsubscribeCodexThreadSkipsEmptyThread(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		t.Fatal("empty thread must not call RPC")
		return nil, nil
	}

	attempted, err := a.UnsubscribeCodexThread(context.Background(), "  ")
	if err != nil || attempted {
		t.Fatalf("attempted=%v err=%v", attempted, err)
	}
}

func TestUnsubscribeCodexThreadReturnsProtocolFailureWithoutRestart(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.markCodexThreadSubscribed("thread-old")
	wantErr := errors.New("unsubscribe unavailable")
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		return nil, wantErr
	}

	attempted, err := a.UnsubscribeCodexThread(context.Background(), "thread-old")
	if !attempted || !errors.Is(err, wantErr) {
		t.Fatalf("attempted=%v err=%v, want protocol failure", attempted, err)
	}
}

func TestUnsubscribeCodexThreadIsIdempotentPerConnection(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.markCodexThreadSubscribed("thread-idle")
	calls := 0
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"status":"unsubscribed"}`), nil
	}

	first, err := a.UnsubscribeCodexThread(context.Background(), "thread-idle")
	if err != nil || !first {
		t.Fatalf("first unsubscribe attempted=%v err=%v", first, err)
	}
	second, err := a.UnsubscribeCodexThread(context.Background(), "thread-idle")
	if err != nil || second || calls != 1 {
		t.Fatalf("second unsubscribe attempted=%v calls=%d err=%v", second, calls, err)
	}
}

func TestUnsubscribeCodexThreadSkipsThreadNotSubscribedByCurrentConnection(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		t.Fatal("an idle binding that was only read must not send thread/unsubscribe")
		return nil, nil
	}

	attempted, err := a.UnsubscribeCodexThread(context.Background(), "thread-idle")
	if err != nil || attempted {
		t.Fatalf("attempted=%v err=%v", attempted, err)
	}
}

func TestCodexThreadSubscriptionIsIndependentFromBindingAndRestoredAfterUnsubscribe(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, StateFile: t.TempDir() + "/state.json",
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	resumeCalls := 0
	unsubscribeCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		case "thread/resume":
			resumeCalls++
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "thread/unsubscribe":
			unsubscribeCalls++
			return json.RawMessage(`{"status":"unsubscribed"}`), nil
		default:
			t.Fatalf("unexpected rpc method %s", method)
			return nil, nil
		}
	}

	if _, err := a.HandoffCodexRuntime(context.Background(), request); err != nil {
		t.Fatalf("HandoffCodexRuntime() error=%v", err)
	}
	if resumeCalls != 0 {
		t.Fatalf("binding resumed thread %d times, want 0", resumeCalls)
	}
	attempted, err := a.SubscribeCodexThread(
		context.Background(), request.Ref.ConversationID, request.Ref.ThreadID,
	)
	if err != nil || !attempted || resumeCalls != 1 {
		t.Fatalf("first subscribe attempted=%v resumeCalls=%d error=%v", attempted, resumeCalls, err)
	}
	attempted, err = a.SubscribeCodexThread(
		context.Background(), request.Ref.ConversationID, request.Ref.ThreadID,
	)
	if err != nil || attempted || resumeCalls != 1 {
		t.Fatalf("second subscribe attempted=%v resumeCalls=%d error=%v", attempted, resumeCalls, err)
	}
	if attempted, err = a.UnsubscribeCodexThread(context.Background(), request.Ref.ThreadID); err != nil || !attempted {
		t.Fatalf("unsubscribe attempted=%v error=%v", attempted, err)
	}
	attempted, err = a.SubscribeCodexThread(
		context.Background(), request.Ref.ConversationID, request.Ref.ThreadID,
	)
	if err != nil || !attempted || resumeCalls != 2 || unsubscribeCalls != 1 {
		t.Fatalf("resubscribe attempted=%v resumeCalls=%d unsubscribeCalls=%d error=%v",
			attempted, resumeCalls, unsubscribeCalls, err)
	}
}

func TestSubscribeCodexThreadUsesDesktopFollowerWhenCodeModeHostOwnsWriter(t *testing.T) {
	a, probe, request := newSubscribeDesktopFallbackAgent(t)
	var resumeCalls int
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/resume" {
			t.Fatalf("unexpected daemon method %q", method)
		}
		resumeCalls++
		return nil, errors.New("agent error: thread thread-1 already has an active writer")
	}

	attempted, err := a.SubscribeCodexThread(
		context.Background(), request.Ref.ConversationID, request.Ref.ThreadID,
	)

	if err != nil || !attempted {
		t.Fatalf("SubscribeCodexThread attempted=%t err=%v, want Desktop follower recovery", attempted, err)
	}
	if resumeCalls != 1 || probe.loadCalls != 1 {
		t.Fatalf("resumeCalls=%d desktopLoads=%d, want one resume and one active-writer history load", resumeCalls, probe.loadCalls)
	}
	binding, ok := a.codexOwners.threadBinding(request.Ref.ThreadID)
	if !ok || binding.Runtime != CodexRuntimeDesktop || binding.State.ActiveTurnID != "turn-desktop" {
		t.Fatalf("binding=%#v found=%t, want verified Desktop active turn", binding, ok)
	}
	a.mu.Lock()
	resumePending := a.resumeOnFirstUse[request.Ref.ConversationID]
	_, daemonSubscribed := a.codexThreadSubscriptions[request.Ref.ThreadID]
	a.mu.Unlock()
	if resumePending || daemonSubscribed {
		t.Fatalf("resumePending=%t daemonSubscribed=%t, want Desktop follower without daemon subscription", resumePending, daemonSubscribed)
	}
}

func TestSubscribeCodexThreadDesktopFallbackDoesNotRestoreClearedConversation(t *testing.T) {
	a, probe, request := newSubscribeDesktopFallbackAgent(t)
	loadEntered := make(chan struct{})
	releaseLoad := make(chan struct{})
	probe.loadFunc = func(context.Context, CodexThreadRef) error {
		close(loadEntered)
		<-releaseLoad
		return nil
	}
	a.rpcCall = subscribeDesktopFallbackRPC(t)

	result := make(chan error, 1)
	go func() {
		_, err := a.SubscribeCodexThread(context.Background(), request.Ref.ConversationID, request.Ref.ThreadID)
		result <- err
	}()
	<-loadEntered
	a.ClearCodexThread(request.Ref.ConversationID)
	close(releaseLoad)

	err := <-result
	if err == nil || !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("SubscribeCodexThread error=%v, want stale binding conflict", err)
	}
	if threadID, ok := a.CurrentCodexThread(request.Ref.ConversationID); ok || threadID != "" {
		t.Fatalf("local binding=(%q,%t), want cleared", threadID, ok)
	}
	if binding, ok := a.codexOwners.currentConversationBinding(request.Ref.ConversationID); ok {
		t.Fatalf("owner binding=%#v, want cleared", binding)
	}
}

func TestSubscribeCodexThreadDesktopFallbackDoesNotOverwriteNewThread(t *testing.T) {
	a, probe, request := newSubscribeDesktopFallbackAgent(t)
	loadEntered := make(chan struct{})
	releaseLoad := make(chan struct{})
	probe.loadFunc = func(context.Context, CodexThreadRef) error {
		close(loadEntered)
		<-releaseLoad
		return nil
	}
	a.rpcCall = subscribeDesktopFallbackRPC(t)

	result := make(chan error, 1)
	go func() {
		_, err := a.SubscribeCodexThread(context.Background(), request.Ref.ConversationID, request.Ref.ThreadID)
		result <- err
	}()
	<-loadEntered
	if err := a.UseCodexThread(context.Background(), request.Ref.ConversationID, "thread-2"); err != nil {
		t.Fatalf("UseCodexThread(thread-2): %v", err)
	}
	close(releaseLoad)

	err := <-result
	if err == nil || !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("SubscribeCodexThread error=%v, want stale binding conflict", err)
	}
	assertCodexConversationThread(t, a, request.Ref.ConversationID, "thread-2")
}

func TestSubscribeCodexThreadDesktopFallbackRejectsClearUseABA(t *testing.T) {
	a, probe, request := newSubscribeDesktopFallbackAgent(t)
	loadEntered := make(chan struct{})
	releaseLoad := make(chan struct{})
	probe.loadFunc = func(context.Context, CodexThreadRef) error {
		close(loadEntered)
		<-releaseLoad
		return nil
	}
	a.rpcCall = subscribeDesktopFallbackRPC(t)

	result := make(chan error, 1)
	go func() {
		_, err := a.SubscribeCodexThread(context.Background(), request.Ref.ConversationID, request.Ref.ThreadID)
		result <- err
	}()
	<-loadEntered
	a.ClearCodexThread(request.Ref.ConversationID)
	if err := a.UseCodexThread(context.Background(), request.Ref.ConversationID, request.Ref.ThreadID); err != nil {
		t.Fatalf("UseCodexThread(thread-1): %v", err)
	}
	close(releaseLoad)

	err := <-result
	if err == nil || !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("SubscribeCodexThread error=%v, want ABA conflict", err)
	}
	assertCodexConversationThread(t, a, request.Ref.ConversationID, request.Ref.ThreadID)
	binding, ok := a.codexOwners.threadBinding(request.Ref.ThreadID)
	if !ok || binding.Runtime != CodexRuntimeWeClaw {
		t.Fatalf("binding=%#v found=%t, want newer WeClaw binding", binding, ok)
	}
}

func TestTrackCodexThreadSubscriptionDoesNotRestoreClearedConversation(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	seedCodexAppServerThreadBinding(a, "conversation-1", "thread-1", true)
	a.ClearCodexThread("conversation-1")

	a.trackCodexThreadSubscription("thread-1")

	if threadID, ok := a.CurrentCodexThread("conversation-1"); ok || threadID != "" {
		t.Fatalf("binding=(%q,%t), want cleared after late subscription callback", threadID, ok)
	}
	a.mu.Lock()
	_, subscribed := a.codexThreadSubscriptions["thread-1"]
	a.mu.Unlock()
	if !subscribed {
		t.Fatal("late callback should still record the connection-local subscription")
	}
}

func newSubscribeDesktopFallbackAgent(t *testing.T) (*ACPAgent, *codexDesktopOwnerProbeFake, CodexRuntimeRequest) {
	t.Helper()
	probe := &codexDesktopOwnerProbeFake{}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe, desktopBridge: true})
	a.codexDesktopCoordination = true
	a.codexDesktopHostSelection = false
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)

	state := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopStateFixture("thread-1", "active")
	raw["turns"] = []any{desktopTurnFixture("turn-desktop", "inProgress", nil)}
	if _, err := state.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	}); err != nil {
		t.Fatal(err)
	}
	a.desktopRuntime = &codexDesktopRuntime{state: state}

	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(request, CodexRuntimeWeClaw, CodexThreadState{
		ThreadID: "thread-1", ThreadStatus: "notLoaded",
	}); err != nil {
		t.Fatal(err)
	}
	seedCodexAppServerThreadBinding(a, request.Ref.ConversationID, request.Ref.ThreadID, true)
	return a, probe, request
}

func subscribeDesktopFallbackRPC(t *testing.T) func(context.Context, string, interface{}) (json.RawMessage, error) {
	t.Helper()
	return func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/resume":
			return nil, errors.New("agent error: thread thread-1 already has an active writer")
		case "thread/read":
			threadID, _ := params.(map[string]interface{})["threadId"].(string)
			return json.RawMessage(`{"thread":{"id":"` + threadID + `","status":{"type":"notLoaded"}}}`), nil
		default:
			t.Fatalf("unexpected daemon method %q", method)
			return nil, nil
		}
	}
}

func assertCodexConversationThread(t *testing.T, a *ACPAgent, conversationID string, wantThreadID string) {
	t.Helper()
	threadID, ok := a.CurrentCodexThread(conversationID)
	if !ok || threadID != wantThreadID {
		t.Fatalf("local binding=(%q,%t), want %q", threadID, ok, wantThreadID)
	}
	binding, ok := a.codexOwners.currentConversationBinding(conversationID)
	if !ok || binding.Ref.ThreadID != wantThreadID {
		t.Fatalf("owner binding=%#v found=%t, want %q", binding, ok, wantThreadID)
	}
}
