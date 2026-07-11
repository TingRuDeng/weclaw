package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	models[0].EffortOptions[0] = "mutated"
	next := DefaultClaudeModels()
	if next[0].ID == "mutated" || next[0].EffortOptions[0] == "mutated" {
		t.Fatal("DefaultClaudeModels returned shared mutable slice")
	}
}

func TestCLIAgentClaudeModelStatusUsesConfiguredModel(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude", Model: "opus", Effort: "high"})

	status := ag.ClaudeModelStatus()

	if status.Model != "opus" || status.Effort != "high" {
		t.Fatalf("status=%#v，期望 model=opus effort=high", status)
	}
}

func TestCLIAgentSetClaudeModelUpdatesFutureSessionConfig(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude", Model: "sonnet", Effort: "medium"})

	ag.SetClaudeModel("opus", "high")
	status := ag.ClaudeModelStatus()

	if status.Model != "opus" || status.Effort != "high" {
		t.Fatalf("status=%#v，期望运行时配置已更新", status)
	}
}

func TestCLIAgentClaudeInvocationKeepsExistingSessionConfig(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude", Model: "sonnet", Effort: "medium"})
	first := ag.claudeInvocationState("conversation-1")
	if first.HasSession || first.Model != "sonnet" || first.Effort != "medium" {
		t.Fatalf("first=%#v，期望新会话捕获初始配置", first)
	}
	ag.mu.Lock()
	ag.sessions["conversation-1"] = "session-1"
	ag.mu.Unlock()

	ag.SetClaudeModel("opus", "high")
	existing := ag.claudeInvocationState("conversation-1")
	if !existing.HasSession || existing.Model != "sonnet" || existing.Effort != "medium" {
		t.Fatalf("existing=%#v，旧会话必须保持创建时配置", existing)
	}

	ag.ClearClaudeSession("conversation-1")
	next := ag.claudeInvocationState("conversation-1")
	if next.HasSession || next.Model != "opus" || next.Effort != "high" {
		t.Fatalf("next=%#v，新会话应使用最新运行时配置", next)
	}
}

func TestCLIAgentClaudeInvocationDoesNotOverrideExternalSession(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude", Model: "opus", Effort: "high"})
	if err := ag.UseClaudeSession(context.Background(), "conversation-1", "external-session"); err != nil {
		t.Fatalf("UseClaudeSession error: %v", err)
	}

	state := ag.claudeInvocationState("conversation-1")

	if !state.HasSession || state.SessionID != "external-session" || state.Model != "" || state.Effort != "" {
		t.Fatalf("state=%#v，恢复的外部会话不应被运行时默认配置覆盖", state)
	}
}

func TestCLIAgentClaudeChatPassesModelAndEffortFlags(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "claude")
	content := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuoteForTest(argsFile) +
		"\nprintf '%s\\n' '{\"type\":\"result\",\"session_id\":\"session-1\",\"result\":\"ok\"}'\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake Claude: %v", err)
	}
	ag := NewCLIAgent(CLIAgentConfig{
		Name: "claude", Command: script, Cwd: dir, Model: "sonnet", Effort: "high",
	})

	if _, err := ag.Chat(context.Background(), "conversation-1", "hello"); err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(data)
	if !strings.Contains(args, "--model\nsonnet\n") || !strings.Contains(args, "--effort\nhigh\n") {
		t.Fatalf("args=%q，期望包含模型和 effort 参数", args)
	}
}
