package agent

import (
	"context"
	"testing"
)

func TestCLIAgentListClaudeModelsReturnsBuiltInCopy(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude", Model: "sonnet"})

	models, err := ag.ListClaudeModels(context.Background())
	if err != nil {
		t.Fatalf("ListClaudeModels error: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("ListClaudeModels returned empty list")
	}
	if models[0].ID != "claude-fable-5" || models[2].Alias != "sonnet" {
		t.Fatalf("models = %#v, want fable first and sonnet alias", models)
	}

	models[0].ID = "mutated"
	next := DefaultClaudeModels()
	if next[0].ID == "mutated" {
		t.Fatal("DefaultClaudeModels returned shared mutable slice")
	}
}

func TestCLIAgentClaudeModelStatusUsesConfiguredModel(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude", Model: "opus"})

	status := ag.ClaudeModelStatus()

	if status.Model != "opus" {
		t.Fatalf("status model=%q, want opus", status.Model)
	}
}
