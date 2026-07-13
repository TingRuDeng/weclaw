package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func boolPtr(b bool) *bool { return &b }

func testDoctorDeps() doctorDeps {
	return doctorDeps{
		lookPath:       func(string) (string, error) { return "/usr/local/bin/agent", nil },
		wechatAccounts: func() (int, error) { return 1, nil },
		feishuCredsOK:  func(string) error { return nil },
		sudoProbe:      func(string) error { return nil },
		claudeACPProbe: func(context.Context, string, config.AgentConfig) error { return nil },
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

func TestDoctorSkipsImplicitWeChatWhenFeishuEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Platforms = map[string]config.PlatformConfig{
		"feishu": {
			Enabled: boolPtr(true),
			Bots: []config.FeishuBotConfig{
				{Name: "main", AppID: "cli_main", AllowedUsers: []string{"ou_main"}},
			},
		},
	}

	results := runDoctorChecks(cfg, testDoctorDeps())
	if _, ok := findResult(results, "platform wechat"); ok {
		t.Fatal("unexpected wechat platform check when feishu enables wechat-off default")
	}
	if _, ok := findResult(results, "access control wechat"); ok {
		t.Fatal("unexpected wechat allowlist check when wechat is not enabled")
	}
}

func TestDoctorChecksEachFeishuBot(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Platforms = map[string]config.PlatformConfig{
		"wechat": {Enabled: boolPtr(false)},
		"feishu": {
			Enabled: boolPtr(true),
			Bots: []config.FeishuBotConfig{
				{Name: "project-a", AppID: "cli_a", AllowedUsers: []string{"ou_a"}},
				{Name: "project-b", AppID: "cli_b"},
			},
		},
	}
	deps := testDoctorDeps()
	checked := make(map[string]bool)
	deps.feishuCredsOK = func(name string) error {
		checked[name] = true
		return nil
	}

	results := runDoctorChecks(cfg, deps)

	if !checked["project-a"] || !checked["project-b"] {
		t.Fatalf("checked=%#v, want both feishu bots checked", checked)
	}
	r, ok := findResult(results, "access control feishu project-b")
	if !ok {
		t.Fatal("missing project-b allowlist check")
	}
	if r.Status != doctorWarn {
		t.Fatalf("project-b allowlist status=%v, want warn", r.Status)
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
	cfg.Agents["claude"] = config.AgentConfig{Type: "acp", Command: "claude-agent-acp"}
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

func TestSummarizeWeclawProcessesWarnsMultipleInstallPaths(t *testing.T) {
	processes := []weclawProcess{
		{PID: 1, Exe: "/usr/local/bin/weclaw", Args: "weclaw start -f"},
		{PID: 2, Exe: "/Users/test/.local/bin/weclaw", Args: "weclaw start"},
	}

	result := summarizeWeclawProcesses(processes)

	if result.Status != doctorWarn {
		t.Fatalf("Status=%v, want warn", result.Status)
	}
	if result.Name != "weclaw processes" {
		t.Fatalf("Name=%q, want weclaw processes", result.Name)
	}
	if got := result.Detail; got == "" || !containsAll(got, "2 process(es)", "2 install path(s)") {
		t.Fatalf("Detail=%q, want process and install path summary", got)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
