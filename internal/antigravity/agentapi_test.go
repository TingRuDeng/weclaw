package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentRunnerInvokesArgumentArray(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "agentapi")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ANTIGRAVITY_APP_DATA_DIR", filepath.Join(dir, "missing"))
	t.Setenv("ANTIGRAVITY_AGENTAPI_EXE", filepath.Join(dir, "must-not-run"))

	runner, err := NewAgentRunner(filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	content := "$(touch " + filepath.Join(dir, "pwned") + ")"
	if err := runner.SendMessage(context.Background(), "conv-1", content); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "send-message" || lines[1] != "conv-1" || lines[2] != content {
		t.Fatalf("agentapi args = %q", lines)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); !os.IsNotExist(err) {
		t.Fatal("content must not be interpreted by a shell")
	}
}

func TestAgentRunnerRejectsConfiguredExecutableOverride(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	override := filepath.Join(dir, "custom-agentapi")
	if err := os.WriteFile(override, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(dir, "empty"))
	t.Setenv("ANTIGRAVITY_APP_DATA_DIR", filepath.Join(dir, "missing"))
	t.Setenv("ANTIGRAVITY_AGENTAPI_EXE", override)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	if _, err := NewAgentRunner(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("runner must not fall back to ANTIGRAVITY_AGENTAPI_EXE")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("ANTIGRAVITY_AGENTAPI_EXE must never be executed")
	}
}

func TestAgentRunnerFallsBackToAppDataBin(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(binDir, "agentapi")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(dir, "empty"))
	runner, err := NewAgentRunner(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runner.Path() != script {
		t.Fatalf("runner path = %q, want %q", runner.Path(), script)
	}
}
