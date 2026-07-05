package config

import "testing"

func TestAgentConfigEffectiveCodexPermissionLevel(t *testing.T) {
	cases := []struct {
		name     string
		cfg      AgentConfig
		approval string
		reviewer string
		sandbox  string
	}{
		{name: "implicit default", cfg: AgentConfig{}, approval: "on-request", reviewer: "user", sandbox: "workspace-write"},
		{name: "default", cfg: AgentConfig{PermissionLevel: "default"}, approval: "on-request", reviewer: "user", sandbox: "workspace-write"},
		{name: "auto review", cfg: AgentConfig{PermissionLevel: "auto_review"}, approval: "on-request", reviewer: "auto_review", sandbox: "workspace-write"},
		{name: "full access", cfg: AgentConfig{PermissionLevel: "full_access"}, approval: "never", sandbox: "danger-full-access"},
		{
			name: "explicit override",
			cfg: AgentConfig{
				PermissionLevel:  "full_access",
				ApprovalPolicy:   "untrusted",
				ApprovalReviewer: "auto_review",
				SandboxMode:      "read-only",
			},
			approval: "untrusted",
			reviewer: "auto_review",
			sandbox:  "read-only",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveApprovalPolicy(); got != tt.approval {
				t.Fatalf("EffectiveApprovalPolicy()=%q, want %q", got, tt.approval)
			}
			if got := tt.cfg.EffectiveApprovalReviewer(); got != tt.reviewer {
				t.Fatalf("EffectiveApprovalReviewer()=%q, want %q", got, tt.reviewer)
			}
			if got := tt.cfg.EffectiveSandboxMode(); got != tt.sandbox {
				t.Fatalf("EffectiveSandboxMode()=%q, want %q", got, tt.sandbox)
			}
		})
	}
}

func TestAgentConfigValidateCodexPermissionConfigRejectsOldLevels(t *testing.T) {
	for _, level := range []string{"request_approval", "auto_approval", "auto", "ask"} {
		t.Run(level, func(t *testing.T) {
			cfg := AgentConfig{PermissionLevel: level}
			if err := cfg.ValidateCodexPermissionConfig(); err == nil {
				t.Fatalf("ValidateCodexPermissionConfig() nil error for %q", level)
			}
		})
	}
}

func TestAgentConfigValidateCodexPermissionConfigRejectsInvalidReviewer(t *testing.T) {
	cfg := AgentConfig{
		PermissionLevel:  "default",
		ApprovalReviewer: "guardian_subagent",
	}
	if err := cfg.ValidateCodexPermissionConfig(); err == nil {
		t.Fatal("ValidateCodexPermissionConfig() nil error for invalid reviewer")
	}
}

func TestConfigValidateIncludesAgentNameForInvalidPermission(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents["codex"] = AgentConfig{PermissionLevel: "auto_approval"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() nil error for invalid agent permission")
	}
	if got := err.Error(); got != `agent "codex": invalid permission_level "auto_approval": use default, auto_review, or full_access` {
		t.Fatalf("Validate() error=%q", got)
	}
}
