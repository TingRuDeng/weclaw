package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareCodexRestartRejectsVisibleDesktopBeforeHostMutation(t *testing.T) {
	dir := newShortCodexHome(t)
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "managed",
		AppServerSocket: filepath.Join(dir, "codex.sock"),
	}, acpAgentOptions{
		desktopBridge: true,
		desktopProbe:  &codexDesktopOwnerProbeFake{socketExists: true, processExists: true},
	})
	called := false
	a.stopManagedHostCall = func(context.Context, string) error {
		called = true
		return nil
	}
	_, err := a.PrepareCodexRestart(context.Background(), func(CodexRestartSnapshot) error { return nil })
	if !errors.Is(err, ErrCodexDesktopFrontendActive) {
		t.Fatalf("PrepareCodexRestart error=%v, want desktop-active rejection", err)
	}
	if called {
		t.Fatal("desktop-active preflight mutated Host")
	}
	if state := a.ensureCodexAppServerGate().stateSnapshot(); state != codexAppServerRunning {
		t.Fatalf("gate state=%s, want running after safe rejection", state)
	}
}

func TestPrepareCodexRestartRejectsWriterLeaseBeforeHostMutation(t *testing.T) {
	dir := newShortCodexHome(t)
	a := newACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "managed",
		AppServerSocket: filepath.Join(dir, "codex.sock"),
	}, acpAgentOptions{desktopBridge: true, desktopProbe: &codexDesktopOwnerProbeFake{}})
	req := CodexRuntimeRequest{
		Ref:    CodexThreadRef{ConversationID: "conversation", ThreadID: "thread-1"},
		Intent: CodexControlIntent{Owner: CodexControlRemote, RouteKey: "route", ConversationID: "conversation", Revision: 1},
	}
	if _, err := a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, CodexThreadState{ThreadID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	lease, err := a.codexOwners.beginTurn(req)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish()
	_, err = a.PrepareCodexRestart(context.Background(), func(CodexRestartSnapshot) error { return nil })
	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("PrepareCodexRestart error=%v, want writer-busy rejection", err)
	}
}

func TestPrepareCodexRestartDoesNotStopHostBeforeIntentIsDurable(t *testing.T) {
	a, _, cleanup := newManagedRestartFixture(t, 11)
	defer cleanup()
	stopped := false
	a.stopManagedHostCall = func(context.Context, string) error {
		stopped = true
		return nil
	}
	want := errors.New("journal unavailable")
	_, err := a.PrepareCodexRestart(context.Background(), func(CodexRestartSnapshot) error {
		return want
	})
	if !errors.Is(err, want) || stopped {
		t.Fatalf("PrepareCodexRestart error=%v stopped=%v", err, stopped)
	}
	if state := a.ensureCodexAppServerGate().stateSnapshot(); state != codexAppServerRunning {
		t.Fatalf("gate state=%s, want running after persistence failure", state)
	}
}

func TestPrepareAndVerifyCodexRestartRequireNewHostGeneration(t *testing.T) {
	a, _, cleanup := newManagedRestartFixture(t, 11)
	defer cleanup()
	stopCalls := 0
	persistedGeneration := uint64(0)
	a.stopManagedHostCall = func(context.Context, string) error {
		if persistedGeneration != 11 {
			t.Fatalf("Host stopped before restart intent persisted: generation=%d", persistedGeneration)
		}
		stopCalls++
		a.mu.Lock()
		a.started = false
		a.mu.Unlock()
		return nil
	}
	snapshot, err := a.PrepareCodexRestart(context.Background(), func(snapshot CodexRestartSnapshot) error {
		persistedGeneration = snapshot.HostGeneration
		return nil
	})
	if err != nil {
		t.Fatalf("PrepareCodexRestart: %v", err)
	}
	if stopCalls != 1 || !snapshot.HostStopped || snapshot.HostGeneration != 11 || persistedGeneration != 11 {
		t.Fatalf("snapshot=%#v stopCalls=%d persisted=%d", snapshot, stopCalls, persistedGeneration)
	}
	if state := a.ensureCodexAppServerGate().stateSnapshot(); state != codexAppServerFailed {
		t.Fatalf("gate state=%s, want failed-closed", state)
	}

	restarted, restartedSocket, cleanupRestarted := newManagedRestartFixture(t, 11)
	defer cleanupRestarted()
	restartedSnapshot := snapshot
	restartedSnapshot.SocketPath = restartedSocket
	restartStops := 0
	restarted.stopManagedHostCall = func(_ context.Context, socketPath string) error {
		restartStops++
		metadata, err := restarted.readCodexHostMetadata(socketPath)
		if err != nil {
			return err
		}
		metadata.Generation = 12
		return restarted.writeCodexHostMetadata(socketPath, metadata)
	}
	recovered, err := restarted.VerifyCodexRestart(context.Background(), restartedSnapshot)
	if err != nil || restartStops != 1 || recovered.HostGeneration != 12 {
		t.Fatalf("VerifyCodexRestart recovery=%#v stops=%d error=%v", recovered, restartStops, err)
	}

	verified, verifiedSocket, cleanupVerified := newManagedRestartFixture(t, 12)
	defer cleanupVerified()
	verifiedSnapshot := snapshot
	verifiedSnapshot.SocketPath = verifiedSocket
	result, err := verified.VerifyCodexRestart(context.Background(), verifiedSnapshot)
	if err != nil {
		t.Fatalf("VerifyCodexRestart new generation: %v", err)
	}
	if result.HostGeneration != 12 || result.HostStopped {
		t.Fatalf("verified snapshot=%#v", result)
	}

	mismatch, _, cleanupMismatch := newManagedRestartFixture(t, 12)
	defer cleanupMismatch()
	mismatch.codexHostMode = "daemon"
	if _, err := mismatch.VerifyCodexRestart(context.Background(), snapshot); !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("VerifyCodexRestart topology mismatch error=%v", err)
	}
}

func newManagedRestartFixture(t *testing.T, generation uint64) (*ACPAgent, string, func()) {
	t.Helper()
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "codex.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(context.Background(), "sleep", "30")
	configureACPProcess(command)
	if err := command.Start(); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	identity, err := inspectCodexHostProcess(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = listener.Close()
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "managed",
		AppServerSocket: socketPath, StateFile: filepath.Join(dir, "state.json"),
	})
	a.codexDesktopPresenceCall = func() (bool, bool) { return false, false }
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/list" {
			return nil, errors.New("unexpected rpc: " + method)
		}
		return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
	}
	a.mu.Lock()
	a.started = true
	a.mu.Unlock()
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerWeClaw, State: "running",
		PID: command.Process.Pid, ProcessGroupID: identity.pgid, UID: identity.uid,
		ProcessStart: identity.start, ObservedCommandHash: identity.commandHash,
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:         socketPath, Generation: generation, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = listener.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_ = listener.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
	}
	return a, socketPath, cleanup
}
