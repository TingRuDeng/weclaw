package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestACPAgentArchivesIdleCodexThreadAndForgetsEveryConversation(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: stateFile,
	})
	seedCodexArchiveTestBindings(a)

	var methods []string
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		methods = append(methods, method)
		values, ok := params.(map[string]interface{})
		if !ok || len(values) != 1 || values["threadId"] != "thread-archive" {
			return nil, fmt.Errorf("%s params=%#v", method, params)
		}
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-archive","status":{"type":"idle"}}}`), nil
		case "thread/archive":
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	if err := a.ArchiveCodexThread(context.Background(), " thread-archive "); err != nil {
		t.Fatalf("ArchiveCodexThread error: %v", err)
	}
	if fmt.Sprint(methods) != "[thread/read thread/archive]" {
		t.Fatalf("rpc methods=%v", methods)
	}
	for _, conversationID := range []string{"conversation-a", "conversation-b"} {
		if threadID, ok := a.CurrentCodexThread(conversationID); ok {
			t.Fatalf("conversation %q still bound to %q", conversationID, threadID)
		}
		if _, ok := a.codexOwners.currentConversationBinding(conversationID); ok {
			t.Fatalf("owner registry still binds conversation %q", conversationID)
		}
	}
	if threadID, ok := a.CurrentCodexThread("conversation-other"); !ok || threadID != "thread-other" {
		t.Fatalf("unrelated conversation=(%q,%v)", threadID, ok)
	}
	if _, ok := a.codexOwners.threadBinding("thread-archive"); ok {
		t.Fatal("archived thread still exists in owner registry")
	}

	persisted := readACPStateFile(t, stateFile)
	if len(persisted.Threads) != 1 || persisted.Threads["conversation-other"] != "thread-other" {
		t.Fatalf("persisted threads=%#v", persisted.Threads)
	}
}

func TestACPAgentRejectsActiveCodexThreadArchive(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	seedCodexArchiveTestBindings(a)
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/read" {
			t.Fatalf("unexpected rpc method: %s", method)
		}
		return json.RawMessage(`{"thread":{"id":"thread-archive","status":{"type":"active"}}}`), nil
	}

	err := a.ArchiveCodexThread(context.Background(), "thread-archive")
	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("archive error=%v, want ErrCodexWriterBusy", err)
	}
	if threadID, ok := a.CurrentCodexThread("conversation-a"); !ok || threadID != "thread-archive" {
		t.Fatalf("active thread binding changed to (%q,%v)", threadID, ok)
	}
}

func TestACPAgentRejectsCodexThreadArchiveWithWriterLease(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(),
	})
	seedCodexArchiveTestBindings(a)
	a.codexOwners.mu.Lock()
	a.codexOwners.leases["thread-archive"] = &codexWriterLeaseState{}
	a.codexOwners.mu.Unlock()
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		t.Fatalf("writer lease should reject before rpc: %s", method)
		return nil, nil
	}

	err := a.ArchiveCodexThread(context.Background(), "thread-archive")
	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("archive error=%v, want ErrCodexWriterBusy", err)
	}
}

func TestACPAgentArchiveUnknownOutcomeForgetsStaleBindings(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	seedCodexArchiveTestBindings(a)
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-archive","status":{"type":"idle"}}}`), nil
		case "thread/archive":
			return nil, errors.New("connection closed after request")
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	err := a.ArchiveCodexThread(context.Background(), "thread-archive")
	if !errors.Is(err, ErrCodexArchiveOutcomeUnknown) {
		t.Fatalf("archive error=%v, want ErrCodexArchiveOutcomeUnknown", err)
	}
	for _, conversationID := range []string{"conversation-a", "conversation-b"} {
		if threadID, ok := a.CurrentCodexThread(conversationID); ok {
			t.Fatalf("uncertain archived conversation %q still bound to %q", conversationID, threadID)
		}
	}
}

func TestACPAgentArchiveReadFailurePreservesBinding(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(),
	})
	seedCodexArchiveTestBindings(a)
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/read" {
			t.Fatalf("unexpected rpc method: %s", method)
		}
		return nil, errors.New("read unavailable")
	}

	err := a.ArchiveCodexThread(context.Background(), "thread-archive")
	if err == nil || errors.Is(err, ErrCodexArchiveOutcomeUnknown) {
		t.Fatalf("archive error=%v, want confirmed preflight failure", err)
	}
	if threadID, ok := a.CurrentCodexThread("conversation-a"); !ok || threadID != "thread-archive" {
		t.Fatalf("preflight failure changed binding to (%q,%v)", threadID, ok)
	}
}

func TestACPAgentConsumesCodexThreadArchivedNotification(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	seedCodexArchiveTestBindings(a)
	observedThreadID := ""
	a.SetCodexThreadArchivedHandler(func(threadID string) {
		observedThreadID = threadID
	})

	handled := a.dispatchCodexKnownNotification(rpcResponse{
		Method: "thread/archived",
		Params: json.RawMessage(`{"threadId":"thread-archive"}`),
	}, "")
	if !handled {
		t.Fatal("thread/archived notification was not handled")
	}
	for _, conversationID := range []string{"conversation-a", "conversation-b"} {
		if threadID, ok := a.CurrentCodexThread(conversationID); ok {
			t.Fatalf("archived notification left conversation %q bound to %q", conversationID, threadID)
		}
	}
	if observedThreadID != "thread-archive" {
		t.Fatalf("archive observer thread=%q", observedThreadID)
	}
}

func seedCodexArchiveTestBindings(a *ACPAgent) {
	a.mu.Lock()
	a.threads["conversation-a"] = "thread-archive"
	a.threads["conversation-b"] = "thread-archive"
	a.threads["conversation-other"] = "thread-other"
	a.resumeOnFirstUse["conversation-a"] = true
	a.resumeOnFirstUse["conversation-b"] = true
	a.mu.Unlock()
	a.codexOwners.claimWeClawConversation(
		CodexThreadRef{ConversationID: "conversation-a", ThreadID: "thread-archive"},
		CodexThreadState{ThreadID: "thread-archive"},
	)
	a.codexOwners.claimWeClawConversation(
		CodexThreadRef{ConversationID: "conversation-b", ThreadID: "thread-archive"},
		CodexThreadState{ThreadID: "thread-archive"},
	)
	a.codexOwners.claimWeClawConversation(
		CodexThreadRef{ConversationID: "conversation-other", ThreadID: "thread-other"},
		CodexThreadState{ThreadID: "thread-other"},
	)
	a.persistState()
}
