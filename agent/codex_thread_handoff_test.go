package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestUnsubscribeCodexThreadDoesNotRestartHost(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.markCodexThreadSubscribed("conversation-old", "thread-old")
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
	a.markCodexThreadSubscribed("conversation-old", "thread-old")
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
	a.markCodexThreadSubscribed("conversation-idle", "thread-idle")
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
