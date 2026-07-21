package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

func TestCodexHostSupervisorRejectsStalePIDIdentityAndLooseMetadata(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "weclaw-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "codex.sock")
	a := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"}, AppServerSocket: socketPath,
	})
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
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, State: "running", PID: cmd.Process.Pid,
		ProcessGroupID: identity.pgid, UID: identity.uid, ProcessStart: identity.start,
		ObservedCommandHash: identity.commandHash, CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath: socketPath, Generation: 4, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	if status := a.InspectCodexHost(context.Background()); !status.Managed || !status.Running || status.Generation != 4 {
		t.Fatalf("managed status=%#v", status)
	}

	metadata.ProcessStart = "reused-pid-start"
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := a.validateManagedCodexHost(socketPath); codexauth.ErrorCode(err) != codexauth.CodeUnmanagedHost {
		t.Fatalf("PID reuse error=%v", err)
	}

	if err := os.Chmod(codexHostMetadataPath(socketPath), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.readCodexHostMetadata(socketPath); err == nil {
		t.Fatal("权限过宽的 Host 元数据必须被拒绝")
	}
}

func TestCodexHostSupervisorRejectsMissingAndStoppedMetadata(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "weclaw-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "codex.sock")
	a := NewACPAgent(ACPAgentConfig{ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"}, AppServerSocket: socketPath})
	if status := a.InspectCodexHost(context.Background()); status.Managed || status.Running || status.Reason == "" {
		t.Fatalf("missing metadata status=%#v", status)
	}
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, State: "stopped", SocketPath: socketPath,
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath), Generation: 2,
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := a.validateManagedCodexHost(socketPath); codexauth.ErrorCode(err) != codexauth.CodeUnmanagedHost {
		t.Fatalf("stopped metadata error=%v", err)
	}
}

func TestMarkCodexHostStoppedRejectsStaleGenerationWithSamePID(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "codex.sock")
	a := NewACPAgent(ACPAgentConfig{ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"}, AppServerSocket: socketPath})
	current := codexHostMetadata{
		Version: codexHostMetadataVersion, State: "running", PID: 4242, ProcessGroupID: 4242,
		UID: uint32(os.Geteuid()), ProcessStart: "current-start", ObservedCommandHash: "current-command",
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath), SocketPath: socketPath,
		Generation: 8, StartedAt: time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC),
	}
	if err := a.writeCodexHostMetadata(socketPath, current); err != nil {
		t.Fatal(err)
	}
	stale := current
	stale.Generation = 7
	stale.ProcessStart = "previous-start"
	a.markCodexHostMetadataStopped(socketPath, stale)

	got, err := a.readCodexHostMetadata(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "running" || got.Generation != current.Generation {
		t.Fatalf("metadata=%#v, stale waiter must not stop current generation", got)
	}
	a.markCodexHostMetadataStopped(socketPath, current)
	got, err = a.readCodexHostMetadata(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "stopped" || got.StoppedAt.IsZero() {
		t.Fatalf("metadata=%#v, current generation should be stopped", got)
	}
}

func TestMarkCodexHostStoppedReturnsMetadataWritePreconditionError(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "codex.sock")
	a := NewACPAgent(ACPAgentConfig{ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"}, AppServerSocket: socketPath})
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, State: "running", PID: 4242, ProcessGroupID: 4242,
		UID: uint32(os.Geteuid()), ProcessStart: "start", ObservedCommandHash: "command",
		CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath), SocketPath: socketPath,
		Generation: 1, StartedAt: time.Date(2026, 7, 21, 16, 0, 0, 0, time.UTC),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	metadataPath := codexHostMetadataPath(socketPath)
	if err := os.Chmod(metadataPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.markCodexHostMetadataStoppedLocked(socketPath, metadata); err == nil {
		t.Fatal("invalid metadata permissions must not be swallowed")
	}
}
