package config

import "testing"

func TestAgentConfigEffectiveCodexPermissionLevel(t *testing.T) {
	cases := []struct {
		name     string
		cfg      AgentConfig
		approval string
		sandbox  string
	}{
		{name: "request approval", cfg: AgentConfig{PermissionLevel: "request_approval"}, approval: "on-request", sandbox: "workspace-write"},
		{name: "auto approval", cfg: AgentConfig{PermissionLevel: "auto_approval"}, approval: "never", sandbox: "workspace-write"},
		{name: "full access", cfg: AgentConfig{PermissionLevel: "full_access"}, approval: "never", sandbox: "danger-full-access"},
		{
			name: "explicit override",
			cfg: AgentConfig{
				PermissionLevel: "full_access",
				ApprovalPolicy:  "untrusted",
				SandboxMode:     "read-only",
			},
			approval: "untrusted",
			sandbox:  "read-only",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveApprovalPolicy(); got != tt.approval {
				t.Fatalf("EffectiveApprovalPolicy()=%q, want %q", got, tt.approval)
			}
			if got := tt.cfg.EffectiveSandboxMode(); got != tt.sandbox {
				t.Fatalf("EffectiveSandboxMode()=%q, want %q", got, tt.sandbox)
			}
		})
	}
}
