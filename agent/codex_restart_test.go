package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReconnectExistingCodexHostForRestartRepairsDeadHostArtifacts(t *testing.T) {
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "codex.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		t.Fatal("unix listener type unavailable")
	}
	unixListener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "managed",
		AppServerSocket: socketPath,
	})
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
	deadPID := 1 << 30
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerWeClaw, State: "running",
		PID: deadPID, ProcessGroupID: deadPID, UID: uint32(os.Geteuid()),
		ProcessStart: "stale-start", ObservedCommandHash: "stale-command",
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:         socketPath, Generation: 7, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}

	if err := a.reconnectExistingCodexHostForRestart(context.Background()); err != nil {
		t.Fatalf("reconnectExistingCodexHostForRestart error=%v, dead Host artifacts should be repaired", err)
	}
	updated, err := a.readCodexHostMetadata(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != "stopped" || updated.Generation != metadata.Generation {
		t.Fatalf("metadata=%#v, want same generation marked stopped", updated)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket remains: %v", err)
	}
}

func TestReconnectExistingCodexHostForRestartRepairsDeadMetadataWithoutSocket(t *testing.T) {
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "codex.sock")
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "managed",
		AppServerSocket: socketPath,
	})
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
	deadPID := 1 << 30
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerWeClaw, State: "running",
		PID: deadPID, ProcessGroupID: deadPID, UID: uint32(os.Geteuid()),
		ProcessStart: "stale-start", ObservedCommandHash: "stale-command",
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:         socketPath, Generation: 7, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}

	if err := a.reconnectExistingCodexHostForRestart(context.Background()); err != nil {
		t.Fatalf("reconnectExistingCodexHostForRestart error=%v, dead metadata should be repaired", err)
	}
	updated, err := a.readCodexHostMetadata(socketPath)
	if err != nil || updated.State != "stopped" || updated.Generation != metadata.Generation {
		t.Fatalf("metadata=%#v err=%v, want same generation marked stopped", updated, err)
	}
}

func TestReconnectExistingCodexHostForRestartPreservesReplacementGeneration(t *testing.T) {
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "codex.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		t.Fatal("unix listener type unavailable")
	}
	unixListener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "managed",
		AppServerSocket: socketPath,
	})
	deadPID := 1 << 30
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerWeClaw, State: "running",
		PID: deadPID, ProcessGroupID: deadPID, UID: uint32(os.Geteuid()),
		ProcessStart: "stale-start", ObservedCommandHash: "stale-command",
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:         socketPath, Generation: 7, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	replacement := metadata
	replacement.Generation++
	replacement.PID++
	replacement.ProcessGroupID++
	replacement.StartedAt = replacement.StartedAt.Add(time.Second)
	a.codexHostConflictPreflightCall = func(context.Context, int) error {
		return a.writeCodexHostMetadata(socketPath, replacement)
	}

	err = a.reconnectExistingCodexHostForRestart(context.Background())
	if err == nil {
		t.Fatal("reconnect error=nil, replacement generation should keep original reconnect failure")
	}
	updated, readErr := a.readCodexHostMetadata(socketPath)
	if readErr != nil || !sameCodexHostGeneration(updated, replacement) || updated.State != "running" {
		t.Fatalf("metadata=%#v readErr=%v, want replacement generation preserved", updated, readErr)
	}
	if _, statErr := os.Lstat(socketPath); statErr != nil {
		t.Fatalf("replacement socket was removed: %v", statErr)
	}
}

func TestReconnectExistingCodexHostForRestartPreservesLiveNonWebSocketListener(t *testing.T) {
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "codex.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
			select {
			case <-done:
				return
			default:
			}
		}
	}()

	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "managed",
		AppServerSocket: socketPath,
	})
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
	deadPID := 1 << 30
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerWeClaw, State: "running",
		PID: deadPID, ProcessGroupID: deadPID, UID: uint32(os.Geteuid()),
		ProcessStart: "stale-start", ObservedCommandHash: "stale-command",
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:         socketPath, Generation: 7, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}

	err = a.reconnectExistingCodexHostForRestart(context.Background())
	if err == nil {
		t.Fatal("reconnect error=nil, live listener must remain a failed-closed preflight")
	}
	updated, readErr := a.readCodexHostMetadata(socketPath)
	if readErr != nil || updated.State != "running" {
		t.Fatalf("metadata=%#v readErr=%v, live listener metadata must remain unchanged", updated, readErr)
	}
	if _, statErr := os.Lstat(socketPath); statErr != nil {
		t.Fatalf("live listener socket was removed: %v", statErr)
	}
}

func TestReconnectExistingCodexHostForRestartKeepsPreMutationFailureSafe(t *testing.T) {
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "codex.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	identity, err := inspectCodexHostProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "managed",
		AppServerSocket: socketPath,
	})
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerWeClaw, State: "running",
		PID: os.Getpid(), ProcessGroupID: pgid, UID: identity.uid,
		ProcessStart: identity.start, ObservedCommandHash: "mismatched-command",
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:         socketPath, Generation: 9, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}

	err = a.reconnectExistingCodexHostForRestart(context.Background())
	if err == nil {
		t.Fatal("reconnect error=nil, want live identity mismatch rejection")
	}
	if errors.Is(err, ErrCodexRestartUnsafe) {
		t.Fatalf("pre-mutation error=%v must not claim an unknown Host stop outcome", err)
	}
	updated, readErr := a.readCodexHostMetadata(socketPath)
	if readErr != nil || updated.State != "running" {
		t.Fatalf("metadata=%#v readErr=%v, live Host record must remain unchanged", updated, readErr)
	}
}

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

func TestForceCloseCodexDesktopAppStopsPresentFrontend(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	present := true
	a.codexDesktopPresenceCall = func() (bool, bool) { return present, present }
	stopCalls := 0
	a.stopDesktopHostCall = func(context.Context) error {
		stopCalls++
		present = false
		return nil
	}

	if err := a.forceCloseCodexDesktopApp(context.Background()); err != nil {
		t.Fatalf("forceCloseCodexDesktopApp: %v", err)
	}
	if stopCalls != 1 || present {
		t.Fatalf("stopCalls=%d present=%v, want App closed once", stopCalls, present)
	}
}

func TestPrepareCodexRestartForceStopsCurrentUnknownHostWithoutManagedIdentity(t *testing.T) {
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "codex.sock")
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: codexHostModeManaged,
		AppServerSocket: socketPath,
	})
	a.codexDesktopPresenceCall = func() (bool, bool) { return false, false }
	a.mu.Lock()
	a.started = true
	a.hostCmd = &exec.Cmd{Process: &os.Process{Pid: 420}}
	a.mu.Unlock()
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)

	uid := uint32(os.Geteuid())
	processes := []codexHostProcessSnapshot{{
		PID: 420, PPID: 1, PGID: 420, UID: uid,
		Executable: "codex", Command: "/opt/custom/codex app-server --listen unix:///tmp/custom.sock",
		Args: []string{"/opt/custom/codex", "app-server", "--listen", "unix:///tmp/custom.sock"},
	}}
	a.codexHostProcessSnapshotCall = func(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
		return processes, nil
	}
	a.codexHostProcessIdentityCall = func(pid int) (codexProcessIdentity, error) {
		if pid != 420 {
			return codexProcessIdentity{}, errors.New("unexpected pid")
		}
		return codexProcessIdentity{uid: uid, pgid: 420, start: "start-420", commandHash: "command-420"}, nil
	}
	stopped := false
	a.stopCodexConflictProcessGroupCall = func(_ context.Context, target codexVerifiedHostConflictTarget) error {
		if target.group.PGID != 420 {
			t.Fatalf("target=%#v, want current unknown Host", target)
		}
		stopped = true
		return nil
	}
	managedStopCalls := 0
	a.stopManagedHostCall = func(context.Context, string) error {
		managedStopCalls++
		return errors.New("force must not require stale management identity")
	}
	persistCalls := 0

	snapshot, err := a.PrepareCodexRestartWithOptions(
		context.Background(),
		func(current CodexRestartSnapshot) error {
			persistCalls++
			if len(current.ConflictingHosts) != 1 || current.ConflictingHosts[0].PGID != 420 {
				t.Fatalf("persisted snapshot=%#v, want current unknown Host", current)
			}
			return nil
		},
		CodexRestartOptions{ForceTerminateCodex: true},
	)
	if err != nil {
		t.Fatalf("PrepareCodexRestartWithOptions force: %v", err)
	}
	if !stopped || managedStopCalls != 0 || persistCalls < 2 {
		t.Fatalf("stopped=%v managedStopCalls=%d persistCalls=%d", stopped, managedStopCalls, persistCalls)
	}
	if len(snapshot.ConflictingHosts) != 1 || !snapshot.ConflictingHosts[0].Stopped {
		t.Fatalf("snapshot=%#v, want stopped current Host", snapshot)
	}
	if a.isRuntimeStarted() || a.runtimePID() != 0 || a.codexRuntimeModeSnapshot() != CodexRuntimeUnknown {
		t.Fatalf("runtime remained attached after force stop: started=%v pid=%d mode=%s", a.isRuntimeStarted(), a.runtimePID(), a.codexRuntimeModeSnapshot())
	}
}

func TestForceCompleteCodexRestartEscalatesPreparedUnknownHost(t *testing.T) {
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "codex.sock")
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: codexHostModeManaged,
		AppServerSocket: socketPath,
	})
	a.codexDesktopPresenceCall = func() (bool, bool) { return false, false }
	a.mu.Lock()
	a.started = true
	a.hostCmd = &exec.Cmd{Process: &os.Process{Pid: 420}}
	a.mu.Unlock()
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)
	a.codexRestartMu.Lock()
	a.codexRestartPrepared = true
	a.codexRestartSnapshot = CodexRestartSnapshot{
		HostMode: codexHostModeManaged, SocketPath: socketPath, HostGeneration: 9, HostStopped: true,
	}
	a.codexRestartMu.Unlock()
	a.ensureCodexAppServerGate().fail(ErrCodexRestartUnsafe)

	uid := uint32(os.Geteuid())
	processes := []codexHostProcessSnapshot{{
		PID: 420, PPID: 1, PGID: 420, UID: uid,
		Executable: "codex", Command: "/opt/custom/codex app-server --listen unix:///tmp/custom.sock",
		Args: []string{"/opt/custom/codex", "app-server", "--listen", "unix:///tmp/custom.sock"},
	}}
	a.codexHostProcessSnapshotCall = func(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
		return processes, nil
	}
	a.codexHostProcessIdentityCall = func(pid int) (codexProcessIdentity, error) {
		return codexProcessIdentity{uid: uid, pgid: 420, start: "start-420", commandHash: "command-420"}, nil
	}
	stopped := false
	a.stopCodexConflictProcessGroupCall = func(context.Context, codexVerifiedHostConflictTarget) error {
		stopped = true
		return nil
	}
	persistCalls := 0

	snapshot, err := a.ForceCompleteCodexRestart(context.Background(), func(current CodexRestartSnapshot) error {
		persistCalls++
		if current.HostGeneration != 9 {
			t.Fatalf("persisted snapshot=%#v, want original generation", current)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ForceCompleteCodexRestart: %v", err)
	}
	if !stopped || persistCalls < 2 || len(snapshot.ConflictingHosts) != 1 || !snapshot.ConflictingHosts[0].Stopped {
		t.Fatalf("stopped=%v persistCalls=%d snapshot=%#v", stopped, persistCalls, snapshot)
	}
	if a.isRuntimeStarted() || a.runtimePID() != 0 || a.codexRuntimeModeSnapshot() != CodexRuntimeUnknown {
		t.Fatalf("runtime remained attached after escalation: started=%v pid=%d mode=%s", a.isRuntimeStarted(), a.runtimePID(), a.codexRuntimeModeSnapshot())
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

func TestPrepareCodexRestartRevalidatesStaleActiveSnapshotAfterClientDisconnect(t *testing.T) {
	a, _, cleanup := newManagedRestartFixture(t, 11)
	defer cleanup()
	req := CodexRuntimeRequest{
		Ref:    CodexThreadRef{ConversationID: "conversation", ThreadID: "thread-stale"},
		Intent: CodexControlIntent{Owner: CodexControlRemote, RouteKey: "route", ConversationID: "conversation", Revision: 1},
	}
	if _, err := a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, CodexThreadState{
		ThreadID: "thread-stale", Active: true, ActiveTurnID: "turn-finished",
	}); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.started = false
	a.mu.Unlock()
	threadListCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/list" {
			return nil, errors.New("unexpected rpc: " + method)
		}
		threadListCalls++
		return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
	}
	stopped := false
	a.stopManagedHostCall = func(context.Context, string) error {
		stopped = true
		return nil
	}

	_, err := a.PrepareCodexRestart(context.Background(), func(CodexRestartSnapshot) error { return nil })
	if err != nil {
		t.Fatalf("PrepareCodexRestart stale snapshot: %v", err)
	}
	if threadListCalls == 0 {
		t.Fatal("重启前没有重新读取 daemon 的权威 thread 状态")
	}
	if !stopped {
		t.Fatal("daemon 已确认空闲后没有继续协调停止 Host")
	}
}

func TestPrepareCodexRestartRejectsAuthoritativeActiveThreadAfterClientDisconnect(t *testing.T) {
	a, _, cleanup := newManagedRestartFixture(t, 11)
	defer cleanup()
	a.mu.Lock()
	a.started = false
	a.mu.Unlock()
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/list" {
			return nil, errors.New("unexpected rpc: " + method)
		}
		return json.RawMessage(`{"data":[{"id":"thread-active","status":{"type":"active"}}],"nextCursor":null}`), nil
	}
	stopped := false
	a.stopManagedHostCall = func(context.Context, string) error {
		stopped = true
		return nil
	}

	_, err := a.PrepareCodexRestart(context.Background(), func(CodexRestartSnapshot) error { return nil })
	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("PrepareCodexRestart error=%v, want authoritative active rejection", err)
	}
	if stopped {
		t.Fatal("权威 thread 状态为 active 时不得停止 Host")
	}
}

func TestPrepareCodexRestartDoesNotUseSharedHostIdleToClearUnknownRuntime(t *testing.T) {
	a, _, cleanup := newManagedRestartFixture(t, 11)
	defer cleanup()
	req := CodexRuntimeRequest{
		Ref:    CodexThreadRef{ConversationID: "conversation", ThreadID: "thread-unknown"},
		Intent: CodexControlIntent{Owner: CodexControlRemote, RouteKey: "route", ConversationID: "conversation", Revision: 1},
	}
	if _, err := a.codexOwners.activateRuntime(req, CodexRuntimeUnknown, CodexThreadState{
		ThreadID: "thread-unknown", Active: true, ActiveTurnID: "turn-unknown",
	}); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.started = false
	a.mu.Unlock()
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/list" {
			return nil, errors.New("unexpected rpc: " + method)
		}
		return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
	}
	stopped := false
	a.stopManagedHostCall = func(context.Context, string) error {
		stopped = true
		return nil
	}

	_, err := a.PrepareCodexRestart(context.Background(), func(CodexRestartSnapshot) error { return nil })
	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("PrepareCodexRestart error=%v, want unknown runtime rejection", err)
	}
	if stopped {
		t.Fatal("shared Host 空闲不能覆盖 unknown runtime 的 active 状态")
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
	mismatchSnapshot := snapshot
	mismatchSnapshot.SocketPath = filepath.Join(t.TempDir(), "old-managed.sock")
	if _, err := mismatch.VerifyCodexRestart(context.Background(), mismatchSnapshot); !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("VerifyCodexRestart topology mismatch error=%v", err)
	}
}

func TestVerifyCodexRestartAllowsStoppedManagedToOfficialDaemonMigration(t *testing.T) {
	a, previousSocket, cleanup := newManagedRestartFixture(t, 21)
	defer cleanup()

	// The persisted transaction was created by the old managed topology. The
	// current process has already been configured for the official daemon and
	// the Codex App is a normal daemon frontend, so Desktop presence is expected.
	codexHome := filepath.Dir(previousSocket)
	currentSocket := codexDaemonSocketPath(codexHome)
	if err := os.MkdirAll(filepath.Dir(currentSocket), 0o700); err != nil {
		t.Fatal(err)
	}
	currentListener, err := net.Listen("unix", currentSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer currentListener.Close()
	a.codexHostMode = codexHostModeDaemon
	a.codexHostSocket = ""
	a.env = map[string]string{"CODEX_HOME": codexHome}
	a.codexDesktopPresenceCall = func() (bool, bool) { return true, true }
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/list" {
			return nil, errors.New("unexpected rpc: " + method)
		}
		return json.RawMessage(`{"data":[{"id":"thread-active","status":{"type":"active"}}],"nextCursor":null}`), nil
	}
	metadata, err := a.readCodexHostMetadata(previousSocket)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Manager = codexHostManagerDaemon
	metadata.SocketPath = currentSocket
	metadata.CommandFingerprint = a.configuredCodexHostCommandFingerprint(currentSocket)
	if err := a.writeCodexHostMetadata(currentSocket, metadata); err != nil {
		t.Fatal(err)
	}

	stopCalls := 0
	a.stopManagedHostCall = func(context.Context, string) error {
		stopCalls++
		return errors.New("must not stop the current daemon during migration")
	}
	previous := CodexRestartSnapshot{
		HostMode:       codexHostModeManaged,
		SocketPath:     previousSocket,
		HostGeneration: 21,
		HostStopped:    true,
	}
	current, err := a.VerifyCodexRestart(context.Background(), previous)
	if err != nil {
		t.Fatalf("VerifyCodexRestart migration: %v", err)
	}
	if stopCalls != 0 {
		t.Fatalf("migration stopped current Host %d time(s)", stopCalls)
	}
	if current.HostMode != codexHostModeDaemon || current.SocketPath != currentSocket ||
		current.HostGeneration != 21 || current.HostStopped {
		t.Fatalf("current snapshot=%#v", current)
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
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
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
