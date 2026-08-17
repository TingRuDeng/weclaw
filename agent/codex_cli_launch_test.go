package agent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareCodexCLILaunchStartsOfficialDaemonAndPinsRemote(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "wc-codex-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	managedCodex := filepath.Join(home, "packages", "standalone", "current", "codex")
	if err := os.MkdirAll(filepath.Dir(managedCodex), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedCodex, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"-c", "feature=true", "app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), Env: map[string]string{"CODEX_HOME": home}, CodexHostMode: "daemon",
	})
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
	var actions []string
	a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
		actions = append(actions, action)
		return codexDaemonLifecycleOutput{
			Status: "started", Backend: "pid", PID: 123,
			ManagedCodexPath: managedCodex, SocketPath: codexDaemonSocketPath(home),
		}, nil
	}

	launch, err := a.PrepareCodexCLILaunch(context.Background(), CodexCLILaunchOptions{
		Cwd: "/tmp/project", Args: []string{"resume", "thread-1"}, AllowHostStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actions, []string{"start"}) {
		t.Fatalf("lifecycle actions=%v, want start", actions)
	}
	wantArgs := []string{"-c", "feature=true", "--remote", "unix://" + codexDaemonSocketPath(home), "resume", "thread-1"}
	if launch.Command != managedCodex || !reflect.DeepEqual(launch.Args, wantArgs) || launch.Cwd != "/tmp/project" {
		t.Fatalf("launch=%#v, want command=%q args=%#v cwd=/tmp/project", launch, managedCodex, wantArgs)
	}
	if !containsEnvValue(launch.Env, "CODEX_HOME", home) {
		t.Fatalf("launch env does not preserve CODEX_HOME: %#v", launch.Env)
	}
}

func TestPrepareCodexCLILaunchRejectsCompatibilityHost(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: "managed",
	})
	_, err := a.PrepareCodexCLILaunch(context.Background(), CodexCLILaunchOptions{AllowHostStart: true})
	if err == nil || !strings.Contains(err.Error(), "official") {
		t.Fatalf("error=%v, want official daemon requirement", err)
	}
}

func TestPrepareCodexCLILaunchDoesNotStartSecondHostForRunningService(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "wc-codex-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	managedCodex := filepath.Join(home, "packages", "standalone", "current", "codex")
	if err := os.MkdirAll(filepath.Dir(managedCodex), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedCodex, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, Env: map[string]string{"CODEX_HOME": home}, CodexHostMode: "daemon",
	})
	a.codexDaemonLifecycleCall = func(context.Context, string) (codexDaemonLifecycleOutput, error) {
		t.Fatal("lifecycle command must not run when a live WeClaw service has no official socket")
		return codexDaemonLifecycleOutput{}, nil
	}

	_, err = a.PrepareCodexCLILaunch(context.Background(), CodexCLILaunchOptions{AllowHostStart: false})
	if err == nil || !strings.Contains(err.Error(), "WeClaw") {
		t.Fatalf("error=%v, want second-host rejection", err)
	}
}

func TestPrepareCodexCLILaunchAllowsVerifiedDaemonWhenDesktopIsPresent(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "wc-codex-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	managedCodex := filepath.Join(home, "packages", "standalone", "current", "codex")
	if err := os.MkdirAll(filepath.Dir(managedCodex), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedCodex, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
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
		Command: "codex", Args: []string{"app-server"}, Env: map[string]string{"CODEX_HOME": home},
		CodexHostMode: "daemon", CodexDesktopBridge: true,
	})
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
	a.desktopProbe = &codexDesktopOwnerProbeFake{socketExists: true, processExists: true}
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)
	a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
		if action != "version" {
			t.Fatalf("daemon lifecycle action=%q, want version", action)
		}
		output := testCodexDaemonOutput("running", "pid", socketPath)
		output.ManagedCodexPath = managedCodex
		return output, nil
	}
	a.codexDaemonMetadataCall = testCodexDaemonMetadata

	launch, err := a.PrepareCodexCLILaunch(context.Background(), CodexCLILaunchOptions{AllowHostStart: false})
	if err != nil || launch.SocketPath != socketPath {
		t.Fatalf("launch=%#v error=%v", launch, err)
	}
	a.setCodexRuntimeMode(CodexRuntimeUnknown)
	if _, err := a.PrepareCodexCLILaunch(context.Background(), CodexCLILaunchOptions{AllowHostStart: false}); err == nil || !strings.Contains(err.Error(), "Codex App") {
		t.Fatalf("unknown authority error=%v, want ambiguous Host rejection", err)
	}
}

func TestPrepareCodexCLILaunchRejectsRemoteOverride(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}, CodexHostMode: "daemon"})
	_, err := a.PrepareCodexCLILaunch(context.Background(), CodexCLILaunchOptions{
		Args: []string{"--remote", "unix:///tmp/other.sock"}, AllowHostStart: true,
	})
	if err == nil || !strings.Contains(err.Error(), "--remote") {
		t.Fatalf("error=%v, want remote override rejection", err)
	}
}

func TestPrepareCodexCLILaunchRejectsConfiguredRemoteOverride(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"--remote=unix:///tmp/other.sock", "app-server"}, CodexHostMode: "daemon",
	})
	_, err := a.PrepareCodexCLILaunch(context.Background(), CodexCLILaunchOptions{AllowHostStart: true})
	if err == nil || !strings.Contains(err.Error(), "--remote") {
		t.Fatalf("error=%v, want configured remote override rejection", err)
	}
}

func TestValidateCodexCLIFrontendArgsRejectsIndependentCommands(t *testing.T) {
	for _, args := range [][]string{
		{"e", "prompt"},
		{"review"},
		{"remote-control", "start"},
		{"app"},
		{"--", "exec", "prompt"},
	} {
		if err := validateCodexCLIFrontendArgs(args); err == nil {
			t.Fatalf("args=%#v should be rejected", args)
		}
	}
	for _, args := range [][]string{
		nil,
		{"-C", "/tmp/project"},
		{"resume", "thread-1"},
		{"fork", "thread-1"},
		{"archive", "thread-1"},
	} {
		if err := validateCodexCLIFrontendArgs(args); err != nil {
			t.Fatalf("args=%#v error=%v, want interactive CLI allowance", args, err)
		}
	}
}

func TestPrepareCodexCLIHostRejectsDesktopAuthority(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}, CodexHostMode: "daemon"})
	a.setCodexRuntimeMode(CodexRuntimeDesktop)

	_, err := a.PrepareCodexCLIHost(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Codex App") {
		t.Fatalf("error=%v, want Desktop authority rejection", err)
	}
}

func containsEnvValue(env []string, key string, value string) bool {
	want := key + "=" + value
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}
