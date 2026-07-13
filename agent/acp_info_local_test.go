package agent

import "testing"

// TestACPInfoExposesLocalCommand 验证 adapter 命令与本地交接命令不会混用。
func TestACPInfoExposesLocalCommand(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	agent := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude",
		Command:        "claude-agent-acp",
		LocalCommand:   "claude",
	})

	info := agent.Info()
	if info.Command != "claude-agent-acp" || info.LocalCommand != "claude" {
		t.Fatalf("Info=%+v, want separate adapter and local commands", info)
	}
}
