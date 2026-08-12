//go:build unix

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexFrontendLeaseBlocksRestartWhileCLIActive(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	first, err := AcquireCodexCLIFrontendLease()
	if err != nil {
		t.Fatalf("AcquireCodexCLIFrontendLease: %v", err)
	}
	defer first.Close()
	second, err := AcquireCodexCLIFrontendLease()
	if err != nil {
		t.Fatalf("second AcquireCodexCLIFrontendLease: %v", err)
	}
	defer second.Close()
	if _, err := AcquireCodexRestartLease(); !errors.Is(err, ErrCodexCLIFrontendActive) {
		t.Fatalf("AcquireCodexRestartLease error=%v, want %v", err, ErrCodexCLIFrontendActive)
	}
}

func TestCodexFrontendLeaseBlocksCLIWhileRecoveryJournalExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	stateDir := filepath.Join(home, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(stateDir, "runtime-restart.json")
	if err := os.WriteFile(journal, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireCodexCLIFrontendLease(); !errors.Is(err, ErrCodexRestartInProgress) {
		t.Fatalf("AcquireCodexCLIFrontendLease error=%v, want recovery fence", err)
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	restart, err := AcquireCodexRestartLease()
	if err != nil {
		t.Fatalf("journal rejection leaked shared lease: %v", err)
	}
	_ = restart.Close()
}

func TestCodexFrontendLeaseBlocksCLIWhileRestartActive(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	restart, err := AcquireCodexRestartLease()
	if err != nil {
		t.Fatalf("AcquireCodexRestartLease: %v", err)
	}
	if _, err := AcquireCodexCLIFrontendLease(); !errors.Is(err, ErrCodexRestartInProgress) {
		t.Fatalf("AcquireCodexCLIFrontendLease error=%v, want %v", err, ErrCodexRestartInProgress)
	}
	if err := restart.Close(); err != nil {
		t.Fatalf("Close restart lease: %v", err)
	}
	cli, err := AcquireCodexCLIFrontendLease()
	if err != nil {
		t.Fatalf("AcquireCodexCLIFrontendLease after release: %v", err)
	}
	_ = cli.Close()
}
