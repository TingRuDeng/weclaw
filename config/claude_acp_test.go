package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestValidateClaudeACPAgentsRejectsEmptyCommand 验证启动前不会放过无法执行的 Claude ACP 配置。
func TestValidateClaudeACPAgentsRejectsEmptyCommand(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents["claude"] = AgentConfig{Type: "acp"}

	err := cfg.ValidateClaudeACPAgents()

	if err == nil || !strings.Contains(err.Error(), "命令") {
		t.Fatalf("ValidateClaudeACPAgents error=%v, want missing command rejection", err)
	}
}

// TestPreflightClaudeACPAgentsValidatesCommandAndCapabilities 验证统一预检覆盖命令解析与必要能力。
func TestPreflightClaudeACPAgentsValidatesCommandAndCapabilities(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents["claude"] = AgentConfig{Type: "acp", Command: "claude-agent-acp"}
	wantProbeErr := errors.New("缺少 session/resume")

	err := cfg.PreflightClaudeACPAgents(ClaudeACPPreflightOptions{
		LookPath: func(command string) (string, error) {
			if command != "claude-agent-acp" {
				t.Fatalf("LookPath command=%q", command)
			}
			return "/fake/bin/claude-agent-acp", nil
		},
		Probe: func(name string, agentCfg AgentConfig) error {
			if name != "claude" || agentCfg.Command != "/fake/bin/claude-agent-acp" {
				t.Fatalf("probe name=%q config=%+v", name, agentCfg)
			}
			return wantProbeErr
		},
	})

	if !errors.Is(err, wantProbeErr) {
		t.Fatalf("PreflightClaudeACPAgents error=%v, want capability failure", err)
	}
}

// TestPreflightClaudeACPAgentsRejectsMissingCommand 验证找不到 adapter 时返回可操作错误。
func TestPreflightClaudeACPAgentsRejectsMissingCommand(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents["claude"] = AgentConfig{Type: "acp", Command: "missing-acp"}

	err := cfg.PreflightClaudeACPAgents(ClaudeACPPreflightOptions{
		LookPath: func(string) (string, error) { return "", fmt.Errorf("not found") },
		Probe:    func(string, AgentConfig) error { t.Fatal("命令缺失时不应探测能力"); return nil },
	})

	if err == nil || !strings.Contains(err.Error(), "missing-acp") {
		t.Fatalf("PreflightClaudeACPAgents error=%v, want missing command detail", err)
	}
}

// TestPreflightClaudeACPAgentsStoresResolvedCommand 验证成功预检会固化绝对命令路径。
func TestPreflightClaudeACPAgentsStoresResolvedCommand(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents["claude"] = AgentConfig{Type: "acp", Command: "claude-agent-acp"}
	probed := false
	err := cfg.PreflightClaudeACPAgents(ClaudeACPPreflightOptions{
		LookPath: func(string) (string, error) { return "/fake/bin/claude-agent-acp", nil },
		Probe: func(_ string, agentCfg AgentConfig) error {
			probed = agentCfg.Command == "/fake/bin/claude-agent-acp"
			return nil
		},
	})
	if err != nil || !probed || cfg.Agents["claude"].Command != "/fake/bin/claude-agent-acp" {
		t.Fatalf("error=%v probed=%t config=%+v", err, probed, cfg.Agents["claude"])
	}
}

// TestPreflightClaudeACPAgentsAllowsMissingClaude 验证未配置 Claude 的用户不受预检影响。
func TestPreflightClaudeACPAgentsAllowsMissingClaude(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.PreflightClaudeACPAgents(ClaudeACPPreflightOptions{}); err != nil {
		t.Fatalf("PreflightClaudeACPAgents error=%v", err)
	}
}
