package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestClassifyCodexHostConflictTargetRecognizesStandaloneDaemonFromExactArgs(t *testing.T) {
	processes := []codexHostProcessSnapshot{{
		PID:        200,
		PPID:       1,
		PGID:       200,
		UID:        501,
		Executable: "codex",
		Command:    "/Users/example/.codex/packages/standalone/current/codex app-server --listen unix://",
		Args: []string{
			"/Users/example/.codex/packages/standalone/current/codex",
			"app-server",
			"--listen",
			"unix://",
		},
	}}

	target := classifyCodexHostConflictTarget(processes, codexHostProcessGroup{
		PGID: 200, UID: 501, PIDs: []int{200}, Kind: "Codex official daemon",
	})

	if target.kind != codexHostConflictTargetOfficialDaemon {
		t.Fatalf("target=%#v, want verified official-daemon candidate", target)
	}
	if target.daemonHome != "/Users/example/.codex" || target.hostPID != 200 {
		t.Fatalf("target=%#v, want daemon home and host pid", target)
	}
}

func TestWaitCodexConflictMembersExitRejectsIdentityDrift(t *testing.T) {
	original := inspectCodexConflictProcessForStop
	t.Cleanup(func() { inspectCodexConflictProcessForStop = original })
	inspectCodexConflictProcessForStop = func(int) (codexProcessIdentity, error) {
		return codexProcessIdentity{uid: 501, pgid: 99, start: "start-b", commandHash: "hash-b"}, nil
	}
	member := codexHostProcessProof{PID: os.Getpid(), UID: 501, PGID: 42, Start: "start-a", CommandHash: "hash-a"}
	if err := waitCodexConflictMembersExit(context.Background(), []codexHostProcessProof{member}, 50*time.Millisecond); !errors.Is(err, errCodexConflictIdentityDrift) {
		t.Fatalf("waitCodexConflictMembersExit error=%v, want identity drift", err)
	}
}

func TestClassifyCodexHostConflictTargetAllowsOfficialDaemonChildInSameProcessTree(t *testing.T) {
	processes := []codexHostProcessSnapshot{
		{
			PID: 200, PPID: 1, PGID: 200, UID: 501,
			Executable: "codex",
			Command:    "/Users/example/.codex/packages/standalone/current/codex app-server --listen unix://",
			Args:       []string{"/Users/example/.codex/packages/standalone/current/codex", "app-server", "--listen", "unix://"},
		},
		{
			PID: 201, PPID: 200, PGID: 200, UID: 501,
			Executable: "codex-code-mode-host",
			Command:    "/Users/example/.codex/packages/standalone/current/bin/codex-code-mode-host",
		},
	}

	target := classifyCodexHostConflictTarget(processes, codexHostProcessGroup{
		PGID: 200, UID: 501, PIDs: []int{200}, Kind: "Codex official daemon",
	})
	if target.kind != codexHostConflictTargetOfficialDaemon || target.hostPID != 200 {
		t.Fatalf("target=%#v, want official daemon with trusted child", target)
	}
}

func TestClassifyCodexHostConflictTargetRecognizesOnlyPrivateAppHostWithClosedProcessGroup(t *testing.T) {
	processes := []codexHostProcessSnapshot{
		{
			PID:        100,
			PPID:       1,
			PGID:       100,
			UID:        501,
			Executable: "ChatGPT",
			Command:    "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT",
		},
		{
			PID:        101,
			PPID:       100,
			PGID:       100,
			UID:        501,
			Executable: "codex",
			Command:    "/Applications/ChatGPT.app/Contents/Resources/codex app-server --listen unix://",
			Args: []string{
				"/Applications/ChatGPT.app/Contents/Resources/codex",
				"app-server",
				"--listen",
				"unix://",
			},
		},
	}

	target := classifyCodexHostConflictTarget(processes, codexHostProcessGroup{
		PGID: 100, UID: 501, PIDs: []int{101}, Kind: "Codex App 私有 Host",
	})

	if target.kind != codexHostConflictTargetAppPrivate || target.appPID != 100 || target.hostPID != 101 {
		t.Fatalf("target=%#v, want closed App-private Host process group", target)
	}
}

func TestClassifyCodexHostConflictTargetRefusesAppHostWhenGroupContainsUnrelatedProcess(t *testing.T) {
	processes := []codexHostProcessSnapshot{
		{
			PID:        100,
			PPID:       1,
			PGID:       100,
			UID:        501,
			Executable: "ChatGPT",
			Command:    "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT",
		},
		{
			PID:        101,
			PPID:       100,
			PGID:       100,
			UID:        501,
			Executable: "codex",
			Command:    "/Applications/ChatGPT.app/Contents/Resources/codex app-server --listen unix://",
			Args:       []string{"/Applications/ChatGPT.app/Contents/Resources/codex", "app-server"},
		},
		{
			PID:        102,
			PPID:       1,
			PGID:       100,
			UID:        501,
			Executable: "unrelated",
			Command:    "/tmp/unrelated-worker",
		},
	}

	target := classifyCodexHostConflictTarget(processes, codexHostProcessGroup{
		PGID: 100, UID: 501, PIDs: []int{101}, Kind: "Codex App 私有 Host",
	})

	if target.kind != codexHostConflictTargetUnknown {
		t.Fatalf("target=%#v, group with an unrelated process must stay untrusted", target)
	}
}

func TestClassifyManagedCodexHostConflictTargetRequiresProtectedMetadata(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("WECLAW_HOME", dataDir)
	runtimeDir := filepath.Join(dataDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(runtimeDir, "other.sock")
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: codexHostModeManaged,
		AppServerSocket: filepath.Join(runtimeDir, "current.sock"),
	})
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerWeClaw, State: "running",
		PID: 42, ProcessGroupID: 42, UID: uint32(os.Geteuid()), ProcessStart: "start-42",
		ObservedCommandHash: "command-42", CommandFingerprint: "fingerprint-42",
		SocketPath: socketPath, Generation: 2, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	a.codexHostProcessIdentityCall = func(pid int) (codexProcessIdentity, error) {
		if pid != 42 {
			return codexProcessIdentity{}, errors.New("unexpected pid")
		}
		return codexProcessIdentity{uid: metadata.UID, pgid: metadata.ProcessGroupID, start: metadata.ProcessStart, commandHash: metadata.ObservedCommandHash}, nil
	}
	processes := []codexHostProcessSnapshot{{
		PID: 42, PPID: 1, PGID: 42, UID: metadata.UID,
		Executable: "codex", Command: "/opt/weclaw/codex app-server --listen unix:///tmp/other.sock",
		Args: []string{"/opt/weclaw/codex", "app-server", "--listen", "unix:///tmp/other.sock"},
	}}
	a.codexHostProcessSnapshotCall = func(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
		return processes, nil
	}
	target, ok := a.classifyManagedCodexHostConflictTarget(context.Background(), processes, codexHostProcessGroup{
		PGID: 42, UID: metadata.UID, PIDs: []int{42}, Kind: "Codex app-server",
	})
	if !ok || target.kind != codexHostConflictTargetManaged || target.hostPID != 42 || target.managedSocket != socketPath {
		t.Fatalf("target=%#v ok=%v, want metadata-backed managed Host", target, ok)
	}
	verified, err := a.verifyCodexHostConflictTarget(context.Background(), target)
	if err != nil || verified.kind != codexHostConflictTargetManaged {
		t.Fatalf("verify managed target=%#v err=%v", verified, err)
	}
}

func TestClassifyManagedCodexHostConflictTargetRejectsUnrelatedProcessInGroup(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("WECLAW_HOME", dataDir)
	runtimeDir := filepath.Join(dataDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(runtimeDir, "managed.sock")
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: codexHostModeManaged,
		AppServerSocket: socketPath,
	})
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerWeClaw, State: "running",
		PID: 42, ProcessGroupID: 42, UID: uint32(os.Geteuid()), ProcessStart: "start-42",
		ObservedCommandHash: "command-42", CommandFingerprint: "fingerprint-42",
		SocketPath: socketPath, Generation: 2, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	a.codexHostProcessIdentityCall = func(pid int) (codexProcessIdentity, error) {
		return codexProcessIdentity{uid: metadata.UID, pgid: metadata.ProcessGroupID, start: metadata.ProcessStart, commandHash: metadata.ObservedCommandHash}, nil
	}
	processes := []codexHostProcessSnapshot{
		{PID: 42, PPID: 1, PGID: 42, UID: metadata.UID, Executable: "codex", Command: "/opt/weclaw/codex app-server", Args: []string{"/opt/weclaw/codex", "app-server"}},
		{PID: 99, PPID: 1, PGID: 42, UID: metadata.UID, Executable: "other", Command: "/tmp/other"},
	}
	target, ok := a.classifyManagedCodexHostConflictTarget(context.Background(), processes, codexHostProcessGroup{
		PGID: 42, UID: metadata.UID, PIDs: []int{42}, Kind: "Codex app-server",
	})
	if ok {
		t.Fatalf("target=%#v ok=%v, want unrelated group member to remain untrusted", target, ok)
	}
}

func TestPrepareCodexRestartWithOptionsStopsVerifiedPrivateAppHostOnlyAfterIntent(t *testing.T) {
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "codex.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	command := exec.CommandContext(context.Background(), "sleep", "30")
	configureACPProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	pgid, err := syscall.Getpgid(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: codexHostModeManaged,
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
	a.hostCmd = command
	a.mu.Unlock()
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerWeClaw, State: "running",
		PID: command.Process.Pid, ProcessGroupID: pgid, UID: uint32(os.Geteuid()),
		ProcessStart: "fixture-start", ObservedCommandHash: "fixture-command",
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:         socketPath, Generation: 31, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	metadata, err = a.readCodexHostMetadata(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	a.codexHostConflictPreflightCall = nil
	appStopped := false
	a.codexDesktopPresenceCall = func() (bool, bool) {
		return !appStopped, !appStopped
	}
	a.codexHostProcessSnapshotCall = func(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
		processes := []codexHostProcessSnapshot{{
			PID: metadata.PID, PPID: 1, PGID: metadata.ProcessGroupID, UID: metadata.UID,
			Executable: "codex", Command: "/opt/weclaw/codex app-server --listen unix:///tmp/weclaw.sock",
			Args: []string{"/opt/weclaw/codex", "app-server", "--listen", "unix:///tmp/weclaw.sock"},
		}}
		if appStopped {
			return processes, nil
		}
		return append(processes,
			codexHostProcessSnapshot{
				PID: 100, PPID: 1, PGID: 100, UID: metadata.UID,
				Executable: "ChatGPT", Command: "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT",
			},
			codexHostProcessSnapshot{
				PID: 101, PPID: 100, PGID: 100, UID: metadata.UID,
				Executable: "codex", Command: "/Applications/ChatGPT.app/Contents/Resources/codex app-server --listen unix://",
				Args: []string{"/Applications/ChatGPT.app/Contents/Resources/codex", "app-server", "--listen", "unix://"},
			},
		), nil
	}
	a.codexHostProcessIdentityCall = func(pid int) (codexProcessIdentity, error) {
		if pid == metadata.PID {
			return codexProcessIdentity{
				uid: metadata.UID, pgid: metadata.ProcessGroupID,
				start: metadata.ProcessStart, commandHash: metadata.ObservedCommandHash,
			}, nil
		}
		return codexProcessIdentity{uid: metadata.UID, pgid: 100, start: "app-start", commandHash: "app-command"}, nil
	}

	intentPersisted := false
	stoppedTargets := 0
	persistCalls := 0
	a.stopConflictingCodexHostCall = func(_ context.Context, target codexVerifiedHostConflictTarget) error {
		if !intentPersisted {
			t.Fatal("private App Host was stopped before restart intent was durable")
		}
		if target.kind != codexHostConflictTargetAppPrivate || target.appPID != 100 || target.hostPID != 101 {
			t.Fatalf("target=%#v, want the verified private App Host", target)
		}
		stoppedTargets++
		appStopped = true
		return nil
	}
	a.stopManagedHostCall = func(context.Context, string) error { return nil }

	snapshot, err := a.PrepareCodexRestartWithOptions(context.Background(), func(snapshot CodexRestartSnapshot) error {
		persistCalls++
		if len(snapshot.ConflictingHosts) != 1 {
			t.Fatalf("snapshot=%#v, want one persisted conflicting Host", snapshot)
		}
		conflict := snapshot.ConflictingHosts[0]
		if conflict.Kind != "Codex App 私有 Host" || conflict.PGID != 100 {
			t.Fatalf("persisted conflict=%#v, want planned private App Host", conflict)
		}
		if persistCalls == 1 && conflict.Stopped {
			t.Fatalf("first persisted conflict=%#v, want stop intent before mutation", conflict)
		}
		intentPersisted = true
		return nil
	}, CodexRestartOptions{StopConflictingCodexHosts: true})
	if err != nil {
		t.Fatalf("PrepareCodexRestartWithOptions: %v", err)
	}
	if stoppedTargets != 1 || !appStopped || !snapshot.HostStopped {
		t.Fatalf("snapshot=%#v stoppedTargets=%d appStopped=%v", snapshot, stoppedTargets, appStopped)
	}
}

func TestStopOfficialDaemonConflictFallsBackToProtectedProcessGroup(t *testing.T) {
	home := newShortCodexHome(t)
	socketPath := codexDaemonSocketPath(home)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(context.Background(), "sleep", "30")
	configureACPProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	pgid, err := syscall.Getpgid(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: codexHostModeDaemon,
		Env: map[string]string{"CODEX_HOME": home}, AppServerSocket: socketPath,
	})
	a.codexHostProcessIdentityCall = func(pid int) (codexProcessIdentity, error) {
		if pid != command.Process.Pid {
			return codexProcessIdentity{}, errors.New("unexpected pid")
		}
		return codexProcessIdentity{
			uid: uint32(os.Geteuid()), pgid: pgid, start: "fixture-start", commandHash: "fixture-command",
		}, nil
	}
	managedBinary := codexDaemonManagedBinaryPath(home)
	a.codexHostProcessSnapshotCall = func(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
		return []codexHostProcessSnapshot{{
			PID: command.Process.Pid, PPID: 1, PGID: pgid, UID: uint32(os.Geteuid()),
			Executable: managedBinary, Command: managedBinary + " app-server --listen unix://",
			Args: []string{managedBinary, "app-server", "--listen", "unix://"},
		}}, nil
	}
	stoppedProcessGroup := false
	a.stopCodexConflictProcessGroupCall = func(_ context.Context, current codexVerifiedHostConflictTarget) error {
		stoppedProcessGroup = true
		if current.hostPID != command.Process.Pid {
			t.Fatalf("stopped target=%#v, want fixture process", current)
		}
		return nil
	}
	a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
		if action == "stop" {
			return codexDaemonLifecycleOutput{}, fmt.Errorf("%w: simulated unsupported lifecycle stop", errCodexDaemonUnmanaged)
		}
		return codexDaemonLifecycleOutput{
			Status: "running", Backend: "pid", PID: command.Process.Pid,
			ManagedCodexPath: managedBinary, SocketPath: socketPath,
		}, nil
	}
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerDaemon, State: "running",
		PID: command.Process.Pid, ProcessGroupID: pgid, UID: uint32(os.Geteuid()),
		ProcessStart: "fixture-start", ObservedCommandHash: "fixture-command",
		CommandFingerprint: "fixture-fingerprint", ManagedCodexPath: managedBinary,
		SocketPath: socketPath, Generation: 1, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	target := codexVerifiedHostConflictTarget{
		codexHostConflictTarget: codexHostConflictTarget{
			kind:    codexHostConflictTargetOfficialDaemon,
			group:   codexHostProcessGroup{PGID: pgid, UID: metadata.UID, PIDs: []int{command.Process.Pid}, Kind: "Codex official daemon"},
			hostPID: command.Process.Pid, daemonHome: home,
		},
		members: []codexHostProcessProof{{
			PID: command.Process.Pid, UID: metadata.UID, PGID: pgid,
			Start: metadata.ProcessStart, CommandHash: metadata.ObservedCommandHash,
			ArgsHash: codexHostArgsHash([]string{managedBinary, "app-server", "--listen", "unix://"}),
		}},
	}

	if err := a.stopOfficialDaemonConflict(context.Background(), target); err != nil {
		t.Fatalf("stopOfficialDaemonConflict: %v", err)
	}
	if !stoppedProcessGroup {
		t.Fatal("protected process-group fallback was not invoked")
	}
	stopped, err := a.readCodexHostMetadata(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != "stopped" {
		t.Fatalf("metadata state=%q, want stopped", stopped.State)
	}
}
