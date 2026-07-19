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
