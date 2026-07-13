package config

import (
	"strings"
	"testing"
)

// TestConfigValidateRejectsClaudeCLI 锁定 Claude 远程后端只能使用 ACP。
func TestConfigValidateRejectsClaudeCLI(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents["claude"] = AgentConfig{Type: "cli", Command: "claude"}

	err := cfg.ValidateClaudeACPAgents()
	if err == nil || !strings.Contains(err.Error(), "weclaw config agent") {
		t.Fatalf("ValidateClaudeACPAgents error=%v, want migration command", err)
	}
}

func TestConfigValidateAllowsMissingOrACPClaude(t *testing.T) {
	if err := (*Config)(nil).ValidateClaudeACPAgents(); err != nil {
		t.Fatalf("nil config error=%v", err)
	}
	cfg := DefaultConfig()
	if err := cfg.ValidateClaudeACPAgents(); err != nil {
		t.Fatalf("missing Claude error=%v", err)
	}
	cfg.Agents["claude"] = AgentConfig{Type: "acp", Command: "claude-agent-acp"}
	if err := cfg.ValidateClaudeACPAgents(); err != nil {
		t.Fatalf("ACP Claude error=%v", err)
	}
	cfg.Agents["claude"] = AgentConfig{Type: " ACP ", Command: "claude-agent-acp"}
	if err := cfg.ValidateClaudeACPAgents(); err == nil {
		t.Fatal("non-canonical ACP type must fail before runtime")
	}
}

// TestDetectAndConfigureClaudeUsesACPAndLocalCommand 验证自动检测不会回退到 CLI 后端。
func TestDetectAndConfigureClaudeUsesACPAndLocalCommand(t *testing.T) {
	withAgentDetection(t, map[string]string{
		"claude-agent-acp": "/fake/bin/claude-agent-acp",
		"claude":           "/fake/bin/claude",
	}, nil)

	cfg := DefaultConfig()
	DetectAndConfigure(cfg)

	got := cfg.Agents["claude"]
	if got.Type != "acp" || got.Command != "/fake/bin/claude-agent-acp" {
		t.Fatalf("claude config=%+v, want ACP adapter", got)
	}
	if got.LocalCommand != "/fake/bin/claude" {
		t.Fatalf("LocalCommand=%q, want native Claude command", got.LocalCommand)
	}
}

// TestDetectAndConfigureClaudeDoesNotFallbackToCLI 验证缺少 adapter 时不注册 Claude。
func TestDetectAndConfigureClaudeDoesNotFallbackToCLI(t *testing.T) {
	withAgentDetection(t, map[string]string{"claude": "/fake/bin/claude"}, nil)

	cfg := DefaultConfig()
	DetectAndConfigure(cfg)

	if _, ok := cfg.Agents["claude"]; ok {
		t.Fatal("Claude adapter missing must not register CLI fallback")
	}
}
