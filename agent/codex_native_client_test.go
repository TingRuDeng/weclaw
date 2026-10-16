package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

func TestNativeCodexSelectionDoesNotDependOnAppOrStandalonePresence(t *testing.T) {
	for _, mode := range []string{"auto", "native"} {
		t.Run(mode, func(t *testing.T) {
			enabled := true
			a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"},
				CodexHostMode: mode, CodexAppDaemon: &enabled, CodexDesktopBridge: true,
				Env: map[string]string{"CODEX_HOME": newShortCodexHome(t)}})
			if a.codexHostMode != "native" || !a.usesOfficialCodexDaemon() {
				t.Fatalf("mode=%s official=%v, want native official client", a.codexHostMode, a.usesOfficialCodexDaemon())
			}
			if a.codexDesktopCoordination || a.codexDesktopHostSelection || a.desktopProbe != nil || a.codexOwners.enforcesControl() {
				t.Fatal("native client must not install Desktop routing or frontend control")
			}
		})
	}
}

func TestNativeCodexAccountChangeDoesNotRestartDaemonDuringInput(t *testing.T) {
	fixture := newAccountSwitchFixture(t)
	markAccountFixtureHostManaged(t, fixture)
	fixture.agent.codexHostMode = codexHostModeNative
	if err := codexauth.WriteAuthFile(fixture.store.AuthPath(), fixture.newSnapshot); err != nil {
		t.Fatal(err)
	}
	err := fixture.agent.ensureCodexAccountForTurn(context.Background())
	if codexauth.ErrorCode(err) != codexauth.CodeBusy || fixture.stopCalls != 0 || fixture.startCalls != 0 {
		t.Fatalf("err=%v stop=%d start=%d, want explicit account action without restart", err, fixture.stopCalls, fixture.startCalls)
	}
}

func TestNativeCodexRecoveryRequiresReplacementWithoutStoppingCurrentHost(t *testing.T) {
	a, previousSocket, cleanup := newManagedRestartFixture(t, 21)
	defer cleanup()
	a.codexHostMode = codexHostModeNative
	a.codexHostSocket = ""
	a.env = map[string]string{"CODEX_HOME": filepath.Dir(previousSocket)}
	socket := codexDaemonSocketPath(filepath.Dir(previousSocket))
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	metadata, err := a.readCodexHostMetadata(previousSocket)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Manager = codexHostManagerDaemon
	metadata.SocketPath = socket
	metadata.CommandFingerprint = a.configuredCodexHostCommandFingerprint(socket)
	if err := a.writeCodexHostMetadata(socket, metadata); err != nil {
		t.Fatal(err)
	}
	a.codexDesktopPresenceCall = func() (bool, bool) { return true, true }
	a.stopManagedHostCall = func(context.Context, string) error { t.Fatal("must not stop external daemon"); return nil }
	previous := CodexRestartSnapshot{HostMode: codexHostModeNative, SocketPath: socket, HostGeneration: 21, HostStopped: true}
	if _, err := a.VerifyCodexRestart(context.Background(), previous); !errors.Is(err, ErrCodexRestartUnsafe) {
		t.Fatalf("err=%v, want unresolved old generation", err)
	}
	metadata.Generation++
	if err := a.writeCodexHostMetadata(socket, metadata); err != nil {
		t.Fatal(err)
	}
	if current, err := a.VerifyCodexRestart(context.Background(), previous); err != nil || current.HostGeneration != 22 {
		t.Fatalf("current=%+v err=%v, want verified replacement", current, err)
	}
}

func TestNativeCodexDoesNotChangeAppEnvironmentOrScanOtherHosts(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"},
		Env: map[string]string{"CODEX_HOME": newShortCodexHome(t)}})
	a.codexHostMode = "native"
	a.codexAppDaemonReuseCall = func(context.Context, bool, string) (codexAppDaemonReuseResult, error) {
		t.Fatal("native client attempted to change App environment")
		return codexAppDaemonReuseResult{}, nil
	}
	a.codexHostConflictPreflightCall = func(context.Context, int) error {
		return errors.New("unrelated App Host must not block a native connection")
	}
	if err := a.applyCodexAppDaemonReusePreference(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureCodexAppReusesDaemon(context.Background(), "/unused"); err != nil {
		t.Fatal(err)
	}
	if err := a.preflightCodexHostConflicts(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCodexProviderPreparationDoesNotReadHistoricalSQLite(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"},
		Env: map[string]string{"CODEX_HOME": newShortCodexHome(t), "CODEX_SQLITE_HOME": filepath.Join(t.TempDir(), "missing")}})
	a.codexHostMode = "native"
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "config/read" {
			t.Fatalf("unexpected method %s", method)
		}
		return json.RawMessage(`{"config":{"model_provider":"openai"}}`), nil
	}
	_, err := a.PrepareCodexThread(context.Background(), CodexRuntimeRequest{
		Ref: CodexThreadRef{ThreadID: "01900000-0000-7000-8000-000000000001"}, WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("native resume must not require local SQLite/provider migration: %v", err)
	}
}

func TestNativeCodexOrdinaryRestartDoesNotStartOrStopDaemon(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"},
		Env: map[string]string{"CODEX_HOME": newShortCodexHome(t)}})
	a.codexHostMode = "native"
	a.rpcCall = func(context.Context, string, interface{}) (json.RawMessage, error) {
		t.Fatal("restart must not require a working daemon")
		return nil, nil
	}
	persisted := false
	snapshot, err := a.PrepareCodexRestart(context.Background(), func(s CodexRestartSnapshot) error {
		persisted = true
		if s.HostStopped {
			t.Fatal("native restart must not stop the external daemon")
		}
		return nil
	})
	if err != nil || !persisted || snapshot.HostStopped {
		t.Fatalf("snapshot=%+v persisted=%v err=%v", snapshot, persisted, err)
	}
	if err := a.CancelCodexRestart(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeDaemonVersionUsesNewPIDLayoutAndRejectsConflictingRecords(t *testing.T) {
	a, socket := newCodexDaemonTestAgent(t)
	a.codexHostMode = codexHostModeNative
	home := filepath.Dir(filepath.Dir(socket))
	identity, err := inspectCodexHostProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	record := codexDaemonPIDRecord{PID: os.Getpid(), ProcessStartTime: identity.start}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(home, "app-server-daemon", "daemon.pid")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	a.codexDaemonLifecycleCall = func(context.Context, string) (codexDaemonLifecycleOutput, error) {
		return codexDaemonLifecycleOutput{Status: "running", SocketPath: socket,
			ManagedCodexPath: filepath.Join(home, "packages", "app-server-daemon", "current", "bin", "codex")}, nil
	}
	output, err := a.runAndValidateCodexDaemonLifecycle(context.Background(), "version", socket)
	if err != nil || output.PID != os.Getpid() || output.Backend != "pid" {
		t.Fatalf("output=%+v err=%v, want new official PID record", output, err)
	}
	record.PID++
	data, _ = json.Marshal(record)
	if err := os.WriteFile(codexDaemonPIDPath(home), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runAndValidateCodexDaemonLifecycle(context.Background(), "version", socket); !errors.Is(err, errCodexDaemonUnmanaged) {
		t.Fatalf("err=%v, want conflicting-record rejection", err)
	}
}
