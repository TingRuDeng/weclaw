package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexCLIAutoUpdateRequiresRepeatedStateRuntimeFailure(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexAutoUpdate: "incompatible",
	})
	calls := 0
	a.codexCLIUpdaterCall = func(context.Context) (codexCLIUpdateResult, error) {
		calls++
		return codexCLIUpdateResult{Before: "0.1.0", After: "0.2.0"}, nil
	}
	stateErr := errors.New("failed to initialize sqlite state runtime")

	retry, err := a.maybeAutoUpdateCodexCLI(context.Background(), stateErr)
	if err != nil || retry || calls != 0 {
		t.Fatalf("first failure=(retry=%v, err=%v, calls=%d), want no update", retry, err, calls)
	}
	retry, err = a.maybeAutoUpdateCodexCLI(context.Background(), stateErr)
	if err != nil || !retry || calls != 1 {
		t.Fatalf("second failure=(retry=%v, err=%v, calls=%d), want one update", retry, err, calls)
	}
}

func TestCodexCLIAutoUpdateFailsClosedWithWriterLease(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexAutoUpdate: "incompatible",
	})
	a.codexOwners.leases["thread-1"] = &codexWriterLeaseState{uncertain: true}
	a.codexStateRuntimeFailures = codexStateRuntimeUpdateThreshold - 1
	called := false
	a.codexCLIUpdaterCall = func(context.Context) (codexCLIUpdateResult, error) {
		called = true
		return codexCLIUpdateResult{}, nil
	}

	retry, err := a.maybeAutoUpdateCodexCLI(
		context.Background(), errors.New("failed to initialize state runtime"),
	)
	if retry || !errors.Is(err, ErrCodexCLIAutoUpdateFailed) || called {
		t.Fatalf("result=(retry=%v, err=%v, called=%v), want fail-closed before update", retry, err, called)
	}
	if !strings.Contains(err.Error(), "未知运行态") {
		t.Fatalf("error=%q, want uncertain lease detail", err)
	}
}

func TestCodexCLIAutoUpdateFailsClosedWhenVersionDoesNotChange(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexAutoUpdate: "incompatible",
	})
	a.codexStateRuntimeFailures = codexStateRuntimeUpdateThreshold - 1
	a.codexCLIUpdaterCall = func(context.Context) (codexCLIUpdateResult, error) {
		return codexCLIUpdateResult{Before: "0.1.0", After: "0.1.0"}, nil
	}

	retry, err := a.maybeAutoUpdateCodexCLI(
		context.Background(), errors.New("failed to initialize sqlite state runtime"),
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
  echo "Error: failed to initialize sqlite state runtime under test CODEX_HOME" >&2
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
	a.codexStateRetryDelay = time.Millisecond
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

func readCodexHostStartCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(data)))
}
