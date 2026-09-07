package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
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

func TestACPAgentArchivedNotificationDeletesOwnerThreadAfterWriterLeaseFinishes(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	seedCodexArchiveTestBindings(a)
	request := CodexRuntimeRequest{Ref: CodexThreadRef{
		ConversationID: "conversation-a", ThreadID: "thread-archive",
	}}
	lease, err := a.codexOwners.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}

	a.handleCodexThreadArchivedNotification(json.RawMessage(`{"threadId":"thread-archive"}`))

	assertCodexConversationCleared(t, a, "conversation-a")
	if _, ok := a.codexOwners.threadBinding("thread-archive"); !ok {
		t.Fatal("active writer lease lost its owner thread before finishing")
	}
	lease.finish()
	if _, ok := a.codexOwners.threadBinding("thread-archive"); ok {
		t.Fatal("finished writer lease left an archived owner thread")
	}
}

func TestForgetCodexThreadTreatsActiveLeaseCleanupAsDeferredSuccess(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	seedCodexArchiveTestBindings(a)
	request := CodexRuntimeRequest{Ref: CodexThreadRef{
		ConversationID: "conversation-a", ThreadID: "thread-archive",
	}}
	lease, err := a.codexOwners.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish()

	if err := a.forgetCodexThread("thread-archive"); err != nil {
		t.Fatalf("forgetCodexThread error=%v, want deferred cleanup success", err)
	}
	assertCodexConversationCleared(t, a, "conversation-a")
	if _, ok := a.codexOwners.threadBinding("thread-archive"); !ok {
		t.Fatal("deferred cleanup removed owner thread before writer lease finished")
	}

	lease.finish()
	if _, ok := a.codexOwners.threadBinding("thread-archive"); ok {
		t.Fatal("deferred cleanup left owner thread after writer lease finished")
	}
}

func TestArchivedThreadIgnoresDesktopSnapshotsUntilUnarchived(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	seedCodexArchiveTestBindings(a)
	a.handleCodexThreadArchivedNotification(json.RawMessage(`{"threadId":"thread-archive"}`))

	a.codexOwners.observeDesktopSnapshot("thread-archive", 2, CodexThreadState{
		ThreadID: "thread-archive", ThreadStatus: "idle",
	})
	if _, ok := a.codexOwners.threadBinding("thread-archive"); ok {
		t.Fatal("late Desktop snapshot recreated archived owner thread")
	}

	a.handleCodexThreadUnarchivedNotification(json.RawMessage(`{"threadId":"thread-archive"}`))
	a.codexOwners.observeDesktopSnapshot("thread-archive", 3, CodexThreadState{
		ThreadID: "thread-archive", ThreadStatus: "idle",
	})
	binding, ok := a.codexOwners.threadBinding("thread-archive")
	if !ok || binding.Runtime != CodexRuntimeDesktop {
		t.Fatalf("owner binding=%#v ok=%t, want new Desktop snapshot after unarchive", binding, ok)
	}
}

func TestACPAgentUnarchivedNotificationAllowsOnlyNewUseGeneration(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	seedCodexArchiveTestBindings(a)
	a.handleCodexThreadArchivedNotification(json.RawMessage(`{"threadId":"thread-archive"}`))

	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	var readCalls atomic.Int32
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/read" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		if readCalls.Add(1) == 1 {
			close(readStarted)
			<-releaseRead
		}
		return json.RawMessage(`{"thread":{"id":"thread-archive","status":{"type":"idle"}}}`), nil
	}
	staleUse := make(chan error, 1)
	go func() {
		staleUse <- a.UseCodexThread(context.Background(), "conversation-new", "thread-archive")
	}()

	<-readStarted
	a.handleCodexThreadUnarchivedNotification(json.RawMessage(`{"threadId":"thread-archive"}`))
	close(releaseRead)
	if err := <-staleUse; !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("stale UseCodexThread error=%v, want lifecycle conflict", err)
	}
	assertCodexConversationCleared(t, a, "conversation-new")

	if err := a.UseCodexThread(context.Background(), "conversation-new", "thread-archive"); err != nil {
		t.Fatalf("new-generation UseCodexThread error=%v", err)
	}
	if threadID, ok := a.CurrentCodexThread("conversation-new"); !ok || threadID != "thread-archive" {
		t.Fatalf("local binding=(%q,%t), want unarchived thread", threadID, ok)
	}
	if binding, ok := a.codexOwners.currentConversationBinding("conversation-new"); !ok || binding.Ref.ThreadID != "thread-archive" {
		t.Fatalf("owner binding=%#v ok=%t, want unarchived thread", binding, ok)
	}
}

func TestACPAgentUnarchivedNotificationCancelsDeferredOwnerThreadDeletion(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	seedCodexArchiveTestBindings(a)
	request := CodexRuntimeRequest{Ref: CodexThreadRef{
		ConversationID: "conversation-a", ThreadID: "thread-archive",
	}}
	lease, err := a.codexOwners.beginTurn(request)
	if err != nil {
		t.Fatal(err)
	}

	a.handleCodexThreadArchivedNotification(json.RawMessage(`{"threadId":"thread-archive"}`))
	a.handleCodexThreadUnarchivedNotification(json.RawMessage(`{"threadId":"thread-archive"}`))
	lease.finish()

	if _, ok := a.codexOwners.threadBinding("thread-archive"); !ok {
		t.Fatal("new unarchived generation was deleted when the old writer lease finished")
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
