package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLICodexRedactsStderrFromReturnedError(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-codex")
	secret := "super-secret-value"
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'api_key="+secret+"\\n' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ag := NewCLIAgent(CLIAgentConfig{Name: "codex", Command: script, Cwd: t.TempDir()})

	_, err := ag.Chat(context.Background(), "conversation-1", "hello")

	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("Chat() error=%v, want sanitized stderr", err)
	}
}
