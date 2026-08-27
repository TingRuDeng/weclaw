package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestOfficialDaemonReconcileAllowsFrontendObservationDuringActiveTurn(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{
		loadErr:       ErrCodexDesktopNoClient,
		socketExists:  true,
		processExists: true,
	}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		CodexHostMode: "daemon", CodexDesktopBridge: true,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)

	first := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(first, CodexRuntimeWeClaw, CodexThreadState{ThreadID: first.Ref.ThreadID}); err != nil {
		t.Fatal(err)
	}
	lease, err := a.codexOwners.beginTurn(first)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish()
	if err := lease.accept("turn-1"); err != nil {
		t.Fatal(err)
	}

	second := remoteCodexRuntimeRequest("thread-1", "route-2", 1)
	second.Ref.ConversationID = "conversation-2"
	second.Intent.ConversationID = "conversation-2"
	binding, err := a.ReconcileCodexObservedTurn(context.Background(), second, CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})

	if err != nil || binding.Runtime != CodexRuntimeWeClaw || !binding.State.Active || binding.State.ActiveTurnID != "turn-1" {
		t.Fatalf("binding=%#v error=%v, official daemon should allow a read-only frontend to observe an active turn", binding, err)
	}
	if current, ok := a.codexOwners.currentConversationBinding("conversation-2"); !ok || current.Ref.ThreadID != "thread-1" {
		t.Fatalf("current=%#v ok=%v, frontend conversation was not bound", current, ok)
	}
}

func TestOfficialDaemonReconcileRejectsDifferentTurnDuringActiveWriter(t *testing.T) {
	probe := &codexDesktopOwnerProbeFake{socketExists: true, processExists: true}
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		CodexHostMode: "daemon", CodexDesktopBridge: true,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	}, acpAgentOptions{desktopProbe: probe})
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)

	first := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	if _, err := a.codexOwners.activateRuntime(first, CodexRuntimeWeClaw, CodexThreadState{ThreadID: first.Ref.ThreadID}); err != nil {
		t.Fatal(err)
	}
	lease, err := a.codexOwners.beginTurn(first)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish()
	if err := lease.accept("turn-1"); err != nil {
		t.Fatal(err)
	}

	second := remoteCodexRuntimeRequest("thread-1", "route-2", 1)
	second.Ref.ConversationID = "conversation-2"
	second.Intent.ConversationID = "conversation-2"
	_, err = a.ReconcileCodexObservedTurn(context.Background(), second, CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-2",
	})

	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("error=%v, mismatched active turn must remain fail-closed", err)
	}
	if _, ok := a.codexOwners.currentConversationBinding("conversation-2"); ok {
		t.Fatal("mismatched observer must not bind a second conversation")
	}
}
