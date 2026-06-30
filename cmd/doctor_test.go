package cmd

import (
	"fmt"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func boolPtr(b bool) *bool { return &b }

func testDoctorDeps() doctorDeps {
	return doctorDeps{
		lookPath:       func(string) (string, error) { return "/usr/local/bin/agent", nil },
		wechatAccounts: func() (int, error) { return 1, nil },
		feishuCredsOK:  func() error { return nil },
		sudoProbe:      func(string) error { return nil },
	}
}

func findResult(results []doctorResult, name string) (doctorResult, bool) {
	for _, r := range results {
		if r.Name == name {
			return r, true
		}
	}
	return doctorResult{}, false
}

func TestDoctorFlagsMissingCLIBinary(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "cli", Command: "claude"}
	deps := testDoctorDeps()
	deps.lookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }

	results := runDoctorChecks(cfg, deps)
	r, ok := findResult(results, `agent "claude"`)
	if !ok {
		t.Fatal("missing agent check result")
	}
	if r.Status != doctorFail {
		t.Fatalf("expected fail for missing binary, got %v", r.Status)
	}
}

func TestDoctorFlagsRunAsUserSudoFailure(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "cli", Command: "claude", RunAsUser: "coder-bot"}
	deps := testDoctorDeps()
	deps.sudoProbe = func(string) error { return fmt.Errorf("no passwordless sudo") }

	results := runDoctorChecks(cfg, deps)
	r, ok := findResult(results, `agent "claude" run_as_user=coder-bot`)
	if !ok {
		t.Fatal("missing run_as_user check result")
	}
	if r.Status != doctorFail {
		t.Fatalf("expected fail for sudo probe failure, got %v", r.Status)
	}
}

func TestDoctorWarnsEmptyAllowlist(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Platforms = map[string]config.PlatformConfig{
		"wechat": {Enabled: boolPtr(true)},
	}
	results := runDoctorChecks(cfg, testDoctorDeps())
	r, ok := findResult(results, "access control wechat")
	if !ok {
		t.Fatal("missing allowlist check")
	}
	if r.Status != doctorWarn {
		t.Fatalf("expected warn for empty allowlist, got %v", r.Status)
	}
}

func TestDoctorFailsNonLoopbackWithoutToken(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.APIAddr = "0.0.0.0:18011"
	results := runDoctorChecks(cfg, testDoctorDeps())
	r, ok := findResult(results, "api server")
	if !ok {
		t.Fatal("missing api server check")
	}
	if r.Status != doctorFail {
		t.Fatalf("expected fail for non-loopback without token, got %v", r.Status)
	}
}

func TestDoctorWarnsEmptyWorkspaceRoots(t *testing.T) {
	cfg := config.DefaultConfig()
	results := runDoctorChecks(cfg, testDoctorDeps())
	r, ok := findResult(results, "workspace confinement")
	if !ok {
		t.Fatal("missing workspace confinement check")
	}
	if r.Status != doctorWarn {
		t.Fatalf("expected warn for empty workspace roots, got %v", r.Status)
	}
}

func TestDoctorWarnsAuditDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	disabled := false
	cfg.AuditLog = &disabled
	results := runDoctorChecks(cfg, testDoctorDeps())
	r, ok := findResult(results, "audit log")
	if !ok {
		t.Fatal("missing audit log check")
	}
	if r.Status != doctorWarn {
		t.Fatalf("expected warn when audit disabled, got %v", r.Status)
	}
}

func TestDoctorPassesHealthyConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "cli", Command: "claude"}
	cfg.Platforms = map[string]config.PlatformConfig{
		"wechat": {Enabled: boolPtr(true), AllowedUsers: []string{"u1"}},
	}
	results := runDoctorChecks(cfg, testDoctorDeps())
	for _, r := range results {
		if r.Status == doctorFail {
			t.Fatalf("unexpected fail: %s — %s", r.Name, r.Detail)
		}
	}
}
