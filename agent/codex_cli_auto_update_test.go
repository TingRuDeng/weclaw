package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexCLIAutoUpdateRequiresRepeatedExplicitCompatibilityFailure(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexAutoUpdate: "incompatible",
	})
	calls := 0
	a.codexCLIUpdaterCall = func(context.Context) (codexCLIUpdateResult, error) {
		calls++
		return codexCLIUpdateResult{Before: "0.1.0", After: "0.2.0"}, nil
	}
	stateErr := errors.New(
		"failed to initialize sqlite state runtime: unsupported schema version; database schema version is newer",
	)

	retry, err := a.maybeAutoUpdateCodexCLI(context.Background(), stateErr)
	if err != nil || retry || calls != 0 {
		t.Fatalf("first failure=(retry=%v, err=%v, calls=%d), want no update", retry, err, calls)
	}
	retry, err = a.maybeAutoUpdateCodexCLI(context.Background(), stateErr)
	if err != nil || !retry || calls != 1 {
		t.Fatalf("second failure=(retry=%v, err=%v, calls=%d), want one update", retry, err, calls)
	}
}

func TestCodexCLIAutoUpdateRejectsAmbiguousStateRuntimeFailures(t *testing.T) {
	failures := map[string]error{
		"generic wrapper": errors.New("failed to initialize sqlite state runtime under test CODEX_HOME"),
		"contention": errors.New(
			"failed to initialize sqlite state runtime: database is locked",
		),
		"corruption": errors.New(
			"failed to initialize sqlite state runtime: database disk image is malformed",
		),
		"readiness timeout": fmt.Errorf("%w after test deadline", errCodexHostStartupTimeout),
	}
	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			a := NewACPAgent(ACPAgentConfig{
				Command: "codex", Args: []string{"app-server"}, CodexAutoUpdate: "incompatible",
			})
			a.codexCompatibilityFailures = codexCompatibilityUpdateThreshold - 1
			called := false
			a.codexCLIUpdaterCall = func(context.Context) (codexCLIUpdateResult, error) {
				called = true
				return codexCLIUpdateResult{Before: "0.1.0", After: "0.2.0"}, nil
			}
			retry, err := a.maybeAutoUpdateCodexCLI(context.Background(), failure)
			if err != nil || retry || called {
				t.Fatalf("result=(retry=%v, err=%v, called=%v), want no update", retry, err, called)
			}
		})
	}
}

func TestCodexCLIAutoUpdateFailsClosedWithWriterLease(t *testing.T) {
	failures := map[string]error{
		"explicit incompatibility": errors.New(
			"failed to initialize state runtime: unsupported database version",
		),
	}
	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			a := NewACPAgent(ACPAgentConfig{
				Command: "codex", Args: []string{"app-server"}, CodexAutoUpdate: "incompatible",
			})
			a.codexOwners.leases["thread-1"] = &codexWriterLeaseState{uncertain: true}
			a.codexCompatibilityFailures = codexCompatibilityUpdateThreshold - 1
			called := false
			a.codexCLIUpdaterCall = func(context.Context) (codexCLIUpdateResult, error) {
				called = true
				return codexCLIUpdateResult{}, nil
			}

			retry, err := a.maybeAutoUpdateCodexCLI(context.Background(), failure)
			if retry || !errors.Is(err, ErrCodexCLIAutoUpdateFailed) || called {
				t.Fatalf("result=(retry=%v, err=%v, called=%v), want fail-closed before update", retry, err, called)
			}
			if !strings.Contains(err.Error(), "未知运行态") {
				t.Fatalf("error=%q, want uncertain lease detail", err)
			}
		})
	}
}

func TestCodexCLIAutoUpdateFailsClosedWhenVersionDoesNotChange(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexAutoUpdate: "incompatible",
	})
	a.codexCompatibilityFailures = codexCompatibilityUpdateThreshold - 1
	a.codexCLIUpdaterCall = func(context.Context) (codexCLIUpdateResult, error) {
		return codexCLIUpdateResult{Before: "0.1.0", After: "0.1.0"}, nil
	}

	retry, err := a.maybeAutoUpdateCodexCLI(
		context.Background(),
		errors.New("failed to initialize sqlite state runtime: requires a newer codex-cli"),
	)
	if retry || !errors.Is(err, ErrCodexCLIAutoUpdateFailed) {
		t.Fatalf("result=(retry=%v, err=%v), want unchanged-version failure", retry, err)
	}
	if !strings.Contains(err.Error(), "版本仍为 0.1.0") {
		t.Fatalf("error=%q, want unchanged version detail", err)
	}
}

func TestACPAgentAutoUpdatesCodexCLIAndRestartsHost(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	dir, err := os.MkdirTemp("/tmp", "weclaw-codex-update-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "app-server.sock")
	markerPath := filepath.Join(dir, "updated")
	countPath := filepath.Join(dir, "starts.log")
	commandPath := filepath.Join(dir, "codex")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  if [ -f "$WECLAW_TEST_CODEX_UPDATE_MARKER" ]; then
    echo "codex-cli 0.2.0"
  else
    echo "codex-cli 0.1.0"
  fi
  exit 0
fi
if [ "$1" = "update" ]; then
  : > "$WECLAW_TEST_CODEX_UPDATE_MARKER"
  exit 0
fi
if [ ! -f "$WECLAW_TEST_CODEX_UPDATE_MARKER" ]; then
  echo "Error: failed to initialize sqlite state runtime under test CODEX_HOME: unsupported schema version; database schema version is newer" >&2
  exit 1
fi
socket=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--listen" ]; then
    shift
    socket="${1#unix://}"
  fi
  shift
done
WECLAW_TEST_CODEX_UNIX_HOST_SOCKET="$socket" exec "$WECLAW_TEST_CODEX_BINARY" -test.run='^TestHelperCodexUnixHost$'
`
	if err := os.WriteFile(commandPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: commandPath, Args: []string{"app-server"},
		AppServerSocket: socketPath, StateFile: filepath.Join(dir, "state.json"),
		CodexAutoUpdate: "incompatible",
		Env: map[string]string{
			"WECLAW_TEST_CODEX_UPDATE_MARKER": markerPath,
			"WECLAW_TEST_CODEX_BINARY":        os.Args[0],
			testCodexUnixHostCountEnv:         countPath,
		},
	})
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
	a.codexCompatibilityRetryWait = time.Millisecond
	t.Cleanup(a.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start() error=%v, want same-request auto-update recovery", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("update marker missing: %v", err)
	}
	if got := readCodexHostStartCount(t, countPath); got != 1 {
		t.Fatalf("host starts=%d, want one compatible start", got)
	}
}

func TestACPAgentDoesNotAutoUpdateCodexCLIAfterHostReadinessTimeout(t *testing.T) {
	a, markerPath, countPath := newCodexHostHangingUntilUpdate(t)
	a.codexHostConnectTimeout = 150 * time.Millisecond
	a.codexCompatibilityRetryWait = time.Millisecond
	updateCalls := 0
	a.codexCLIUpdaterCall = func(context.Context) (codexCLIUpdateResult, error) {
		updateCalls++
		if err := os.WriteFile(markerPath, []byte("updated"), 0o600); err != nil {
			return codexCLIUpdateResult{}, err
		}
		return codexCLIUpdateResult{Before: "0.1.0", After: "0.2.0"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.Start(ctx)
	if err == nil {
		t.Fatal("Start() error=nil, want readiness timeout")
	}
	if updateCalls != 0 {
		t.Fatalf("update calls=%d, want 0", updateCalls)
	}
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("update marker unexpectedly exists: %v", statErr)
	}
	if _, statErr := os.Stat(countPath); !os.IsNotExist(statErr) {
		t.Fatalf("compatible host unexpectedly started: %v", statErr)
	}
}

func TestACPAgentDoesNotAutoUpdateCodexCLIWhenCallerDeadlineExpires(t *testing.T) {
	a, _, _ := newCodexHostHangingUntilUpdate(t)
	a.codexHostConnectTimeout = time.Second
	updateCalls := 0
	a.codexCLIUpdaterCall = func(context.Context) (codexCLIUpdateResult, error) {
		updateCalls++
		return codexCLIUpdateResult{Before: "0.1.0", After: "0.2.0"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err := a.Start(ctx)
	if !errors.Is(err, errCodexHostStartupTimeout) {
		t.Fatalf("Start() error=%v, want detached shared startup readiness timeout", err)
	}
	if updateCalls != 0 {
		t.Fatalf("update calls=%d, want 0 for caller cancellation", updateCalls)
	}
}

func TestWaitForCodexHostClassifiesNearDeadlineProcessExitBeforeTimeout(t *testing.T) {
	cmd := exec.CommandContext(
		context.Background(),
		"sh",
		"-c",
		"printf 'ready\\n'; sleep 0.075",
	)
	configureACPProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper readiness=%q error=%v", line, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()

	socketPath := filepath.Join(t.TempDir(), "never-created.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := waitForCodexHost(ctx, socketPath, cmd.Process.Pid, done, 25*time.Millisecond)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("waitForCodexHost() unexpectedly connected")
	}
	if err == nil || errors.Is(err, errCodexHostStartupTimeout) {
		t.Fatalf("waitForCodexHost() error=%v, want ordinary process exit", err)
	}
	if isCodexCLICompatibilityFailure(err) {
		t.Fatalf("ordinary near-deadline exit was classified as an auto-update candidate: %v", err)
	}
}

func newCodexHostHangingUntilUpdate(t *testing.T) (*ACPAgent, string, string) {
	t.Helper()
	t.Setenv("WECLAW_HOME", t.TempDir())
	dir, err := os.MkdirTemp("/tmp", "weclaw-codex-timeout-update-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "app-server.sock")
	markerPath := filepath.Join(dir, "updated")
	countPath := filepath.Join(dir, "starts.log")
	commandPath := filepath.Join(dir, "codex")
	script := `#!/bin/sh
if [ ! -f "$WECLAW_TEST_CODEX_UPDATE_MARKER" ]; then
  WECLAW_TEST_CODEX_HANG=1 exec "$WECLAW_TEST_CODEX_BINARY" -test.run='^TestHelperCodexHangingHost$'
fi
socket=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--listen" ]; then
    shift
    socket="${1#unix://}"
  fi
  shift
done
WECLAW_TEST_CODEX_UNIX_HOST_SOCKET="$socket" exec "$WECLAW_TEST_CODEX_BINARY" -test.run='^TestHelperCodexUnixHost$'
`
	if err := os.WriteFile(commandPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: commandPath, Args: []string{"app-server"},
		AppServerSocket: socketPath, StateFile: filepath.Join(dir, "state.json"),
		CodexAutoUpdate: "incompatible",
		Env: map[string]string{
			"WECLAW_TEST_CODEX_UPDATE_MARKER": markerPath,
			"WECLAW_TEST_CODEX_BINARY":        os.Args[0],
			testCodexUnixHostCountEnv:         countPath,
		},
	})
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
	t.Cleanup(a.Stop)
	return a, markerPath, countPath
}

func TestHelperCodexHangingHost(t *testing.T) {
	if os.Getenv("WECLAW_TEST_CODEX_HANG") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func readCodexHostStartCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(data)))
}
