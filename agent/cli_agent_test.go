package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIAgentClaudeSessionControl(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude"})

	if err := ag.UseClaudeSession(context.Background(), "conversation-1", "session-1"); err != nil {
		t.Fatalf("UseClaudeSession error: %v", err)
	}
	sessionID, ok := ag.CurrentClaudeSession("conversation-1")
	if !ok || sessionID != "session-1" {
		t.Fatalf("CurrentClaudeSession=(%q,%v), want session-1 true", sessionID, ok)
	}

	ag.ClearClaudeSession("conversation-1")

	if sessionID, ok := ag.CurrentClaudeSession("conversation-1"); ok || sessionID != "" {
		t.Fatalf("session should be cleared, got (%q,%v)", sessionID, ok)
	}
}

func TestCLIAgentClaudeSessionControlAllowsCustomClaudeCommandName(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "sonnet", Command: "/usr/local/bin/claude"})

	if err := ag.UseClaudeSession(context.Background(), "conversation-1", "session-1"); err != nil {
		t.Fatalf("UseClaudeSession error: %v", err)
	}
	sessionID, ok := ag.CurrentClaudeSession("conversation-1")
	if !ok || sessionID != "session-1" {
		t.Fatalf("CurrentClaudeSession=(%q,%v), want session-1 true", sessionID, ok)
	}

	ag.ClearClaudeSession("conversation-1")

	if sessionID, ok := ag.CurrentClaudeSession("conversation-1"); ok || sessionID != "" {
		t.Fatalf("session should be cleared, got (%q,%v)", sessionID, ok)
	}
}

func TestCLIAgentConversationCwdOverridesGlobalCwd(t *testing.T) {
	dir := t.TempDir()
	workspaceA := filepath.Join(dir, "workspace-a")
	workspaceB := filepath.Join(dir, "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("mkdir workspace A: %v", err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("mkdir workspace B: %v", err)
	}
	recordPath := filepath.Join(dir, "pwd.txt")
	scriptPath := filepath.Join(dir, "fake-codex")
	script := "#!/bin/sh\npwd > " + shellQuoteForTest(recordPath) + "\necho ok\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	ag := NewCLIAgent(CLIAgentConfig{Name: "codex", Command: scriptPath, Cwd: workspaceB})
	ag.SetConversationCwd("conversation-a", workspaceA)
	ag.SetCwd(workspaceB)

	if _, err := ag.Chat(context.Background(), "conversation-a", "hello"); err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	recorded, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read recorded pwd: %v", err)
	}
	if got := string(recorded); got != workspaceA+"\n" {
		t.Fatalf("pwd=%q, want %q", got, workspaceA+"\n")
	}
}

func shellQuoteForTest(value string) string {
	quoted := "'"
	for _, r := range value {
		if r == '\'' {
			quoted += "'\\''"
			continue
		}
		quoted += string(r)
	}
	return quoted + "'"
}
