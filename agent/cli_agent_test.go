package agent

import (
	"context"
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
