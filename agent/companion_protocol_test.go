package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupCompanionEndpointsRemovesEndpointFilesOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	workspace := t.TempDir()
	endpoint := CompanionEndpoint{
		Agent:   "codex",
		Host:    "127.0.0.1",
		Port:    12345,
		Token:   "token",
		Cwd:     workspace,
		Command: "codex",
	}
	if err := writeCompanionEndpoint(endpoint); err != nil {
		t.Fatalf("writeCompanionEndpoint() error = %v", err)
	}
	notesPath := filepath.Join(home, "companions", "notes.txt")
	if err := os.WriteFile(notesPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write notes file: %v", err)
	}

	if err := CleanupCompanionEndpoints(); err != nil {
		t.Fatalf("CleanupCompanionEndpoints() error = %v", err)
	}

	if _, err := ReadCompanionEndpoint("codex", workspace); err == nil {
		t.Fatal("ReadCompanionEndpoint() error = nil, want endpoint removed")
	}
	if _, err := os.Stat(notesPath); err != nil {
		t.Fatalf("notes file should be kept: %v", err)
	}
}

func TestWriteCompanionEndpointDoesNotLeaveTempFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	endpoint := CompanionEndpoint{
		Agent:   "codex",
		Host:    "127.0.0.1",
		Port:    12345,
		Token:   "token",
		Cwd:     t.TempDir(),
		Command: "codex",
	}

	if err := writeCompanionEndpoint(endpoint); err != nil {
		t.Fatalf("writeCompanionEndpoint() error = %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(home, "companions", "*.tmp"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files = %#v, want none", matches)
	}
}
