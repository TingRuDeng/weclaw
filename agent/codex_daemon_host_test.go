package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

func TestCodexDaemonCommandArgsPreserveRootOverrides(t *testing.T) {
	got := codexDaemonCommandArgs(
		[]string{"-c", `model="gpt-test"`, "app-server", "--listen", "stdio://"},
		"start",
	)
	want := []string{"-c", `model="gpt-test"`, "app-server", "daemon", "start"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codexDaemonCommandArgs()=%#v, want %#v", got, want)
	}
}

func TestParseCodexDaemonLifecycleOutputRequiresSingleJSONValue(t *testing.T) {
	valid := `{"status":"running","backend":"pid","managedCodexPath":"/tmp/codex","socketPath":"/tmp/codex.sock"}`
	output, err := parseCodexDaemonLifecycleOutput(valid)
	if err != nil || output.Status != "running" || output.Backend != "pid" {
		t.Fatalf("output=%#v error=%v", output, err)
	}
	if _, err := parseCodexDaemonLifecycleOutput(valid + "\n{}"); err == nil {
		t.Fatal("multiple lifecycle JSON values were accepted")
	}
}

func TestCodexDaemonProcessCommandRequiresExactManagedBinary(t *testing.T) {
	const managed = "/tmp/codex/current/codex"
	for _, valid := range []string{
		managed + " app-server --listen unix://",
		managed + " app-server",
	} {
		if !codexDaemonProcessCommandMatches(valid, managed) {
			t.Fatalf("valid command rejected: %q", valid)
		}
	}
	for _, invalid := range []string{
		"/tmp/codex/current/codex-old app-server --listen unix://",
		"wrapper " + managed + " app-server",
		managed + " app-server-malicious",
		managed,
	} {
		if codexDaemonProcessCommandMatches(invalid, managed) {
			t.Fatalf("invalid command accepted: %q", invalid)
		}
	}
}

func TestCodexDaemonLifecycleCommandUsesStandaloneBinary(t *testing.T) {
	a, socketPath := newCodexDaemonTestAgent(t)
	if _, err := a.resolveCodexDaemonLifecycleCommand(); !errors.Is(err, errCodexDaemonInstallRequired) {
		t.Fatalf("missing standalone error=%v", err)
	}
	home := filepath.Dir(filepath.Dir(socketPath))
	binary := codexDaemonManagedBinaryPath(home)
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := a.resolveCodexDaemonLifecycleCommand()
	if err != nil {
		t.Fatal(err)
	}
	if got != binary {
		t.Fatalf("lifecycle command=%q, want standalone %q", got, binary)
	}
}

func TestCodexDaemonClientAttachesOnlyToOfficialManagedSocket(t *testing.T) {
	t.Run("managed daemon", func(t *testing.T) {
		a, socketPath := newCodexDaemonTestAgent(t)
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatal(err)
		}
		server := newFakeCodexHost(listener)
		server.start(t)
		t.Cleanup(func() { _ = listener.Close() })

		var actions []string
		a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
			actions = append(actions, action)
			return testCodexDaemonOutput("running", "pid", socketPath), nil
		}
		a.codexDaemonMetadataCall = testCodexDaemonMetadata
		if err := a.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actions, []string{"version"}) {
			t.Fatalf("actions=%v, want version only", actions)
		}
	})

	t.Run("unmanaged socket", func(t *testing.T) {
		a, socketPath := newCodexDaemonTestAgent(t)
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatal(err)
		}
		server := newFakeCodexHost(listener)
		server.start(t)
		t.Cleanup(func() { _ = listener.Close() })
		a.codexDaemonLifecycleCall = func(context.Context, string) (codexDaemonLifecycleOutput, error) {
			return testCodexDaemonOutput("running", "", socketPath), nil
		}
		a.codexDaemonMetadataCall = testCodexDaemonMetadata

		err = a.Start(context.Background())
		if !errors.Is(err, errCodexDaemonUnmanaged) {
			t.Fatalf("Start() error=%v, want unmanaged daemon rejection", err)
		}
		if a.isRuntimeStarted() {
			t.Fatal("unmanaged socket was attached")
		}
	})
}

func TestCodexDaemonClientStartsOfficialDaemonWithoutLegacyFallback(t *testing.T) {
	a, socketPath := newCodexDaemonTestAgent(t)
	var actions []string
	var listener net.Listener
	a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
		actions = append(actions, action)
		if action != "start" {
			t.Fatalf("action=%q, want start", action)
		}
		var err error
		listener, err = net.Listen("unix", socketPath)
		if err != nil {
			return codexDaemonLifecycleOutput{}, err
		}
		server := newFakeCodexHost(listener)
		server.start(t)
		return testCodexDaemonOutput("started", "pid", socketPath), nil
	}
	a.codexDaemonMetadataCall = testCodexDaemonMetadata
	t.Cleanup(func() {
		if listener != nil {
			_ = listener.Close()
		}
	})

	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actions, []string{"start"}) {
		t.Fatalf("actions=%v, want one start", actions)
	}
	if a.hostCmd != nil {
		t.Fatal("official daemon client unexpectedly owns a legacy Host process")
	}
}

func TestCodexDaemonStartFailureNeverFallsBackToLegacyHost(t *testing.T) {
	a, _ := newCodexDaemonTestAgent(t)
	var actions []string
	a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
		actions = append(actions, action)
		return codexDaemonLifecycleOutput{}, errors.New("official daemon start denied")
	}

	err := a.Start(context.Background())
	if err == nil || !reflect.DeepEqual(actions, []string{"start"}) {
		t.Fatalf("Start() error=%v actions=%v", err, actions)
	}
	if a.hostCmd != nil {
		t.Fatal("official daemon failure started a legacy Host")
	}
}

func TestCodexDaemonStopUsesOfficialLifecycleAndRecordsTerminalMetadata(t *testing.T) {
	a, socketPath := newCodexDaemonTestAgent(t)
	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	configureACPProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	identity, err := inspectCodexHostProcess(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(filepath.Dir(socketPath))
	pidPath := codexDaemonPIDPath(home)
	writeTestCodexDaemonPIDRecord(t, pidPath, codexDaemonPIDRecord{
		PID: cmd.Process.Pid, ProcessStartTime: identity.start,
	})
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerDaemon,
		State: "running", PID: cmd.Process.Pid, ProcessGroupID: identity.pgid,
		UID: identity.uid, ProcessStart: identity.start, ObservedCommandHash: identity.commandHash,
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:         socketPath, Generation: 4, ManagedCodexPath: "/tmp/managed-codex",
		StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	var actions []string
	a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
		actions = append(actions, action)
		if action == "stop" {
			if err := os.Remove(pidPath); err != nil {
				return codexDaemonLifecycleOutput{}, err
			}
			return testCodexDaemonOutput("stopped", "pid", socketPath), nil
		}
		return testCodexDaemonOutput("running", "pid", socketPath), nil
	}

	if err := a.stopManagedCodexHostLocked(context.Background(), socketPath); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actions, []string{"version", "stop"}) {
		t.Fatalf("actions=%v", actions)
	}
	stopped, err := a.readCodexHostMetadata(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != "stopped" || stopped.StoppedAt.IsZero() {
		t.Fatalf("metadata=%#v", stopped)
	}
}

func TestCodexDaemonStopRejectsGenerationCreatedDuringStop(t *testing.T) {
	a, socketPath := newCodexDaemonTestAgent(t)
	home := filepath.Dir(filepath.Dir(socketPath))
	pidPath := codexDaemonPIDPath(home)
	expected := codexHostMetadata{PID: 100, ProcessStart: "old"}
	writeTestCodexDaemonPIDRecord(t, pidPath, codexDaemonPIDRecord{PID: 101, ProcessStartTime: "new"})
	err := a.verifyCodexDaemonStopped(context.Background(), socketPath, expected)
	if codexauth.ErrorCode(err) != codexauth.CodeConflict {
		t.Fatalf("verifyCodexDaemonStopped() error=%v", err)
	}
}

func TestCodexHostAutoModeUsesStandaloneDaemonOnlyWhenAvailable(t *testing.T) {
	t.Run("standalone available", func(t *testing.T) {
		home := t.TempDir()
		binary := codexDaemonManagedBinaryPath(home)
		if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("test"), 0o700); err != nil {
			t.Fatal(err)
		}
		a := NewACPAgent(ACPAgentConfig{
			Command: "codex", Args: []string{"app-server"}, CodexHostMode: "auto",
			Env: map[string]string{"CODEX_HOME": home},
		})
		if a.codexHostMode != codexHostModeDaemon {
			t.Fatalf("mode=%q, want daemon", a.codexHostMode)
		}
	})

	t.Run("standalone absent", func(t *testing.T) {
		a := NewACPAgent(ACPAgentConfig{
			Command: "codex", Args: []string{"app-server"}, CodexHostMode: "auto",
			Env: map[string]string{"CODEX_HOME": t.TempDir()},
		})
		if a.codexHostMode != codexHostModeManaged {
			t.Fatalf("mode=%q, want managed compatibility backend", a.codexHostMode)
		}
	})

	t.Run("existing official socket", func(t *testing.T) {
		home := newShortCodexHome(t)
		socketPath := codexDaemonSocketPath(home)
		if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		a := NewACPAgent(ACPAgentConfig{
			Command: "codex", Args: []string{"app-server"}, CodexHostMode: "auto",
			Env: map[string]string{"CODEX_HOME": home},
		})
		if a.codexHostMode != codexHostModeDaemon {
			t.Fatalf("mode=%q, want daemon", a.codexHostMode)
		}
	})
}

func newCodexDaemonTestAgent(t *testing.T) (*ACPAgent, string) {
	t.Helper()
	home := newShortCodexHome(t)
	socketPath := codexDaemonSocketPath(home)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "daemon",
		Env: map[string]string{"CODEX_HOME": home}, StateFile: filepath.Join(home, "state.json"),
	})
	t.Cleanup(a.Stop)
	return a, socketPath
}

func newShortCodexHome(t *testing.T) string {
	t.Helper()
	// macOS limits Unix socket paths to roughly 104 bytes. testing.T.TempDir
	// includes the full test name and can exceed that limit before the behavior
	// under test is reached.
	home, err := os.MkdirTemp("/tmp", "wc-codex-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

func testCodexDaemonOutput(status, backend, socketPath string) codexDaemonLifecycleOutput {
	return codexDaemonLifecycleOutput{
		Status: status, Backend: backend, ManagedCodexPath: "/tmp/managed-codex",
		SocketPath: socketPath, AppServerVersion: "test",
	}
}

func testCodexDaemonMetadata(
	_ context.Context,
	output codexDaemonLifecycleOutput,
	socketPath string,
) (codexHostMetadata, error) {
	return codexHostMetadata{
		Version: codexHostMetadataVersion, Manager: codexHostManagerDaemon,
		State: "running", PID: os.Getpid(), ProcessGroupID: os.Getpid(),
		UID: uint32(os.Geteuid()), ProcessStart: "test", ObservedCommandHash: "test",
		CommandFingerprint: "test", SocketPath: socketPath, Generation: 1,
		ManagedCodexPath: output.ManagedCodexPath, AppServerVersion: output.AppServerVersion,
		StartedAt: time.Now().UTC(),
	}, nil
}

func writeTestCodexDaemonPIDRecord(t *testing.T, path string, record codexDaemonPIDRecord) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
