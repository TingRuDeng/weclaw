package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

// TestRunConfigAgentMigratesLegacyClaude 验证旧 CLI 配置可在无法正常加载时原地迁移。
func TestRunConfigAgentMigratesLegacyClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	legacy := `{"default_agent":"claude","agents":{"claude":{"type":"cli","command":"claude","args":["--dangerously-skip-permissions"],"aliases":["cc"],"cwd":"/tmp/project","env":{"TOKEN":"secret"},"model":"sonnet","effort":"high","progress":{"mode":"stream"}}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runConfigAgent(configAgentOptions{
		Name: "claude", Command: "/bin/claude-agent-acp", LocalCommand: "/bin/claude",
		LookPath: func(command string) (string, error) { return command, nil },
		Probe:    func(string, config.AgentConfig) error { return nil },
	})
	if err != nil {
		t.Fatalf("runConfigAgent error: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	got := cfg.Agents["claude"]
	if got.Type != "acp" || got.Command != "/bin/claude-agent-acp" || got.LocalCommand != "/bin/claude" {
		t.Fatalf("migrated config=%+v", got)
	}
	if len(got.Args) != 0 {
		t.Fatalf("Args=%#v, want cleared legacy CLI args", got.Args)
	}
	if got.Cwd != "/tmp/project" || got.Model != "sonnet" || got.Effort != "high" || got.Env["TOKEN"] != "secret" {
		t.Fatalf("legacy fields not preserved: %+v", got)
	}
	if got.Progress == nil || got.Progress.Mode != "stream" || len(got.Aliases) != 1 || got.Aliases[0] != "cc" {
		t.Fatalf("progress or aliases not preserved: %+v", got)
	}
}

// TestRunConfigAgentRejectsBrokenClaudeAdapter 验证能力握手失败时不会覆盖原配置。
func TestRunConfigAgentRejectsBrokenClaudeAdapter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	original := `{"agents":{"claude":{"type":"cli","command":"claude"}}}`
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("initialize 失败")
	err := runConfigAgent(configAgentOptions{
		Name: "claude", Command: "/bin/claude-agent-acp",
		LookPath: func(command string) (string, error) { return command, nil },
		Probe:    func(string, config.AgentConfig) error { return wantErr },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runConfigAgent error=%v, want %v", err, wantErr)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatalf("config=%q error=%v, want unchanged", data, err)
	}
}

// TestResolveConfigAgentOptions 验证默认发现与显式路径错误边界。
func TestResolveConfigAgentOptions(t *testing.T) {
	lookPath := func(command string) (string, error) {
		paths := map[string]string{
			"claude-agent-acp": "/bin/claude-agent-acp",
			"claude":           "/bin/claude",
		}
		if path := paths[command]; path != "" {
			return path, nil
		}
		return "", fmt.Errorf("not found")
	}

	got, err := resolveConfigAgentOptions(configAgentOptions{LookPath: lookPath})
	if err != nil || got.Command != "/bin/claude-agent-acp" || got.LocalCommand != "/bin/claude" {
		t.Fatalf("options=%+v error=%v", got, err)
	}
	if _, err := resolveConfigAgentOptions(configAgentOptions{Name: "other", LookPath: lookPath}); err == nil {
		t.Fatal("non-Claude agent without command must fail")
	}
	if _, err := resolveConfigAgentOptions(configAgentOptions{
		Name: "claude", Command: "claude-agent-acp", LocalCommand: "missing", LookPath: lookPath,
	}); err == nil {
		t.Fatal("explicit missing local command must fail")
	}
}

// TestResolveConfigAgentOptionsAllowsMissingAutoLocalCommand 验证本地交接缺失不阻断远程 ACP。
func TestResolveConfigAgentOptionsAllowsMissingAutoLocalCommand(t *testing.T) {
	lookPath := func(command string) (string, error) {
		if command == "claude-agent-acp" {
			return "/bin/claude-agent-acp", nil
		}
		return "", fmt.Errorf("not found")
	}

	got, err := resolveConfigAgentOptions(configAgentOptions{LookPath: lookPath})
	if err != nil || got.LocalCommand != "" {
		t.Fatalf("options=%+v error=%v, want remote-only config", got, err)
	}
}

func TestConfigAgentCommandRejectsPositionArguments(t *testing.T) {
	if err := configAgentCmd.Args(configAgentCmd, []string{"codex"}); err == nil {
		t.Fatal("position argument must be rejected")
	}
}

// TestProbeConfigAgentBoundaries 验证非 Claude 跳过探针且 Claude 缺少探针时显式失败。
func TestProbeConfigAgentBoundaries(t *testing.T) {
	if err := probeConfigAgent(configAgentOptions{Name: "codex"}, config.AgentConfig{}); err != nil {
		t.Fatalf("non-Claude probe error=%v", err)
	}
	if err := probeConfigAgent(configAgentOptions{Name: "claude"}, config.AgentConfig{}); err == nil {
		t.Fatal("Claude 缺少能力探针时必须失败")
	}
}
