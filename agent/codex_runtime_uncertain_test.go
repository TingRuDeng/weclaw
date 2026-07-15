package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMarkCodexRuntimeConflictPersistsUntilExplicitHandoff(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	req := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	state := CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1"}
	if _, err := a.codexOwners.activateRuntime(req, CodexRuntimeDesktop, state); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkCodexRuntimeConflict(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	binding, ok := a.codexOwners.threadBinding("thread-1")
	if !ok || binding.Runtime != CodexRuntimeConflict || binding.State.ActiveTurnID != "turn-1" || binding.ConflictReason == "" {
		t.Fatalf("binding=%#v ok=%v", binding, ok)
	}
	if _, err := a.InspectCodexRuntime(context.Background(), req); !errors.Is(err, ErrCodexRuntimeConflict) {
		t.Fatalf("inspect error=%v", err)
	}
	binding, _ = a.codexOwners.threadBinding("thread-1")
	if binding.Runtime != CodexRuntimeConflict {
		t.Fatalf("普通 Inspect 不应清除 conflict: %#v", binding)
	}
	desktop := req
	desktop.Intent = CodexControlIntent{Owner: CodexControlDesktop, Revision: 2}
	binding, err := a.HandoffCodexRuntime(context.Background(), desktop)
	if err != nil || binding.Runtime == CodexRuntimeConflict || binding.ConflictReason != "" {
		t.Fatalf("explicit handoff binding=%#v error=%v", binding, err)
	}
}

func TestMarkedCodexRuntimeConflictRejectsAllThreadOperations(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{loadErr: ErrCodexDesktopNoClient}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	req := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(req, CodexRuntimeDesktop, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkCodexRuntimeConflict(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	assertCodexConflictError(t, func() error {
		_, err := a.ReadCodexThreadState(context.Background(), req.Ref.ConversationID, req.Ref.ThreadID)
		return err
	}())
	assertCodexConflictError(t, a.SteerCodexThread(context.Background(), req.Ref.ConversationID, req.Ref.ThreadID, "turn-1", "继续"))
	assertCodexConflictError(t, a.InterruptCodexThread(context.Background(), req.Ref.ConversationID, req.Ref.ThreadID, "turn-1"))
	_, watchErr := a.WatchCodexThread(context.Background(), req.Ref.ConversationID, req.Ref.ThreadID, nil)
	assertCodexConflictError(t, watchErr)
	_, runErr := a.RunCodexTurn(context.Background(), CodexTurnRequest{Runtime: req, Message: "继续"})
	assertCodexConflictError(t, runErr)
}

func TestMarkCodexRuntimeConflictInvalidatesWriterLease(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	req := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	lease, err := a.codexOwners.beginTurn(req)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish()
	if err := a.MarkCodexRuntimeConflict(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	assertCodexConflictError(t, lease.check())
}

func TestMarkCodexRuntimeConflictIgnoresCanceledCleanupContext(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: &codexDesktopOwnerProbeFake{}})
	req := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.MarkCodexRuntimeConflict(ctx, req); err != nil {
		t.Fatal(err)
	}
	if binding, ok := a.codexOwners.threadBinding("thread-1"); !ok || binding.Runtime != CodexRuntimeConflict {
		t.Fatalf("binding=%#v ok=%v", binding, ok)
	}
}

func assertCodexConflictError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrCodexRuntimeConflict) {
		t.Fatalf("error=%v, want conflict", err)
	}
}
