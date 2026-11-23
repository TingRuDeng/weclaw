package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

const codexGateTestTimeout = 300 * time.Millisecond

func TestCodexAppServerGateExclusiveIsNonWaitingAndCanFailClosed(t *testing.T) {
	gate := newCodexAppServerGate()
	permit, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.beginExclusive(); !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("beginExclusive() error=%v", err)
	}
	permit.release()
	if err := gate.beginExclusive(); err != nil {
		t.Fatal(err)
	}
	gate.finishExclusive(false, false)
	if gate.stateSnapshot() != codexAppServerFailed {
		t.Fatalf("state=%s", gate.stateSnapshot())
	}
	if _, err := gate.acquire(context.Background()); !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("acquire failed gate error=%v", err)
	}
}

func TestCodexAppServerGateForcedExclusiveRecoversFailedStateWithoutReopeningIt(t *testing.T) {
	gate := newCodexAppServerGate()
	gate.fail(errors.New("runtime lost"))

	if err := gate.beginForcedExclusive(); err != nil {
		t.Fatalf("beginForcedExclusive() error=%v, explicit force should enter recovery", err)
	}
	if state := gate.stateSnapshot(); state != codexAppServerDraining {
		t.Fatalf("state=%s, want draining during forced recovery", state)
	}

	gate.finishExclusive(false, true)
	if state := gate.stateSnapshot(); state != codexAppServerFailed {
		t.Fatalf("state=%s, failed runtime must remain fail-closed after an unsuccessful force", state)
	}
}

func TestCodexAppServerGateIgnoresUnsafeCodexHomeUntilAccountIndexExists(t *testing.T) {
	weclawHome := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex")
	if err := os.Mkdir(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECLAW_HOME", weclawHome)
	socketPath := filepath.Join(os.TempDir(), "weclaw-gate-test.sock")
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		AppServerSocket: socketPath, StateFile: filepath.Join(t.TempDir(), "state.json"),
		Env: map[string]string{"CODEX_HOME": codexHome},
	})

	gate := a.ensureCodexAppServerGate()
	permit, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatalf("account index 不存在时 acquire() error=%v", err)
	}
	permit.release()
	if gate.stateSnapshot() != codexAppServerRunning {
		t.Fatalf("gate state=%s, want running", gate.stateSnapshot())
	}
	if err := a.validateCodexAccountForWrite(context.Background()); err != nil {
		t.Fatalf("account index 不存在时 validateCodexAccountForWrite() error=%v", err)
	}
}

func TestCodexAppServerGateReportsUnsafeCodexHomeWhenAccountIndexExists(t *testing.T) {
	weclawHome := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex")
	if err := os.Mkdir(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECLAW_HOME", weclawHome)
	socketPath := filepath.Join(os.TempDir(), "weclaw-gate-test.sock")
	hostID, err := codexauth.HostID(codexHome, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(weclawHome, "codex-accounts", hostID, "index.json")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		AppServerSocket: socketPath, StateFile: filepath.Join(t.TempDir(), "state.json"),
		Env: map[string]string{"CODEX_HOME": codexHome},
	})

	_, err = a.ensureCodexAppServerGate().acquire(context.Background())
	if !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("acquire() error=%v, want runtime unavailable", err)
	}
	if !strings.Contains(err.Error(), "CODEX_HOME") || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("acquire() error=%v, want retained safety cause", err)
	}
}

func waitForCodexGateState(t *testing.T, gate *codexAppServerGate, want codexAppServerGateState) {
	t.Helper()
	deadline := time.Now().Add(codexGateTestTimeout)
	for time.Now().Before(deadline) {
		if gate.stateSnapshot() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("gate state=%s，want %s", gate.stateSnapshot(), want)
}
