package web

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
)

func boolPtr(b bool) *bool { return &b }

func TestRedactConfigHidesSecrets(t *testing.T) {
	cfg := &config.Config{APIToken: "super-secret-token", Agents: map[string]config.AgentConfig{
		"claude": {Type: "acp", Command: "claude-agent-acp", LocalCommand: "claude", APIKey: "sk-ant-xxx", Env: map[string]string{"ANTHROPIC_API_KEY": "sk-zzz"}},
	}}
	view := redactConfig(cfg)
	blob, _ := json.Marshal(view)
	for _, secret := range []string{"super-secret-token", "sk-ant-xxx", "sk-zzz"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("redacted view leaked secret %q: %s", secret, blob)
		}
	}
	if view.APIToken != secretMask || view.Agents["claude"].APIKey != secretMask || view.Agents["claude"].Env["ANTHROPIC_API_KEY"] != secretMask {
		t.Fatal("non-empty secrets should be masked")
	}
}

func TestMergeViewPreservesMaskedSecrets(t *testing.T) {
	current := &config.Config{APIToken: "keep-token", Agents: map[string]config.AgentConfig{
		"claude": {Type: "acp", Command: "claude-agent-acp", LocalCommand: "claude", APIKey: "keep-key", Env: map[string]string{"K": "keep-val"}, PermissionLevel: "auto_review", ApprovalPolicy: "on-request", ApprovalReviewer: "auto_review", SandboxMode: "workspace-write"},
	}}
	view := redactConfig(current)
	agentView := view.Agents["claude"]
	agentView.Command = "claude-2"
	view.Agents["claude"] = agentView
	merged := mergeView(current, view)
	agentCfg := merged.Agents["claude"]
	if merged.APIToken != "keep-token" || agentCfg.APIKey != "keep-key" || agentCfg.Env["K"] != "keep-val" {
		t.Fatalf("masked secrets must be preserved: %+v", agentCfg)
	}
	if agentCfg.PermissionLevel != "auto_review" || agentCfg.ApprovalPolicy != "on-request" || agentCfg.ApprovalReviewer != "auto_review" || agentCfg.SandboxMode != "workspace-write" {
		t.Fatalf("permission fields must be preserved: %+v", agentCfg)
	}
	if agentCfg.Command != "claude-2" || agentCfg.LocalCommand != "claude" {
		t.Fatalf("non-secret fields not round-tripped: %+v", agentCfg)
	}
}

func TestMergeViewOverwritesNewSecret(t *testing.T) {
	current := &config.Config{APIToken: "old"}
	view := redactConfig(current)
	view.APIToken = "new-token"
	if got := mergeView(current, view).APIToken; got != "new-token" {
		t.Fatalf("APIToken=%q, want new-token", got)
	}
}

func TestMergeViewUpdatesAdminUsers(t *testing.T) {
	current := &config.Config{AdminUsers: []string{"old_admin"}}
	view := redactConfig(current)
	view.AdminUsers = []string{"new_admin"}
	if got := mergeView(current, view).AdminUsers; !reflect.DeepEqual(got, []string{"new_admin"}) {
		t.Fatalf("AdminUsers=%#v, want new_admin", got)
	}
}

func TestPlatformTopologyChanged(t *testing.T) {
	current := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(false)}}}
	soft := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(false), AllowedUsers: []string{"u1"}}}}
	if restartRequiredConfigChanged(current, soft) {
		t.Fatal("allowed_users change is soft")
	}
	topology := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(true)}}}
	if !restartRequiredConfigChanged(current, topology) {
		t.Fatal("enabling a platform must require restart")
	}
}

func TestPlatformTopologyChangedDetectsFeishuBotList(t *testing.T) {
	current := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(true), Bots: []config.FeishuBotConfig{{Name: "project-a", AppID: "cli_a", AllowedUsers: []string{"ou_a"}}}}}}
	soft := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(true), Bots: []config.FeishuBotConfig{{Name: "project-a", AppID: "cli_a", AllowedUsers: []string{"ou_b"}}}}}}
	topology := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(true), Bots: []config.FeishuBotConfig{{Name: "project-a", AppID: "cli_b"}}}}}
	if restartRequiredConfigChanged(current, soft) || !restartRequiredConfigChanged(current, topology) {
		t.Fatal("bot soft/topology change classification mismatch")
	}
}

func TestPlatformTopologyChangedDetectsNonReloadableConfig(t *testing.T) {
	base := config.DefaultConfig()
	base.APIAddr = "127.0.0.1:18011"
	base.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex", Model: "gpt-old"}
	tests := []func(*config.Config){
		func(cfg *config.Config) { cfg.APIAddr = "127.0.0.1:19011" },
		func(cfg *config.Config) { cfg.SaveDir = "/tmp/output" },
		func(cfg *config.Config) { cfg.AuditLogPath = "/tmp/audit.log" },
		func(cfg *config.Config) {
			agentCfg := cfg.Agents["codex"]
			agentCfg.Model = "gpt-new"
			cfg.Agents["codex"] = agentCfg
		},
	}
	for index, mutate := range tests {
		next := mergeView(base, redactConfig(base))
		mutate(next)
		if !restartRequiredConfigChanged(base, next) {
			t.Fatalf("case %d must require restart", index)
		}
	}
}

func TestPlatformTopologyChangedIgnoresSoftAgentProgress(t *testing.T) {
	base := config.DefaultConfig()
	base.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex"}
	next := mergeView(base, redactConfig(base))
	agentCfg := next.Agents["codex"]
	progress := config.DefaultProgressConfig()
	agentCfg.Progress = &progress
	next.Agents["codex"] = agentCfg
	if restartRequiredConfigChanged(base, next) {
		t.Fatal("agent progress change is soft")
	}
}

func TestPlatformStatusesIncludeEachFeishuBot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := feishu.SaveCredentialsForBot("project-a", feishu.Credentials{AppID: "cli_a", AppSecret: "secret-a"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	cfg := config.DefaultConfig()
	cfg.Platforms["feishu"] = config.PlatformConfig{Enabled: &enabled, Bots: []config.FeishuBotConfig{{Name: "project-a", AppID: "cli_a", AllowedUsers: []string{"ou_a"}}, {Name: "project-b", AppID: "cli_b"}}}
	statuses := platformStatuses(cfg)
	first, firstOK := findPlatformStatus(statuses, "feishu/project-a")
	second, secondOK := findPlatformStatus(statuses, "feishu/project-b")
	if !firstOK || !first.CredentialsPresent || first.AllowedUsersCount != 1 || !secondOK || second.CredentialsPresent || second.AllowedUsersCount != 0 {
		t.Fatalf("platform statuses=%#v", statuses)
	}
}

func findPlatformStatus(statuses []platformStatus, name string) (platformStatus, bool) {
	for _, status := range statuses {
		if status.Name == name {
			return status, true
		}
	}
	return platformStatus{}, false
}

func TestValidateConfigRejectsBadAgent(t *testing.T) {
	if err := validateConfig(&config.Config{Agents: map[string]config.AgentConfig{"x": {Type: "cli"}}}); err == nil {
		t.Fatal("cli agent without command should fail")
	}
	if err := validateConfig(&config.Config{Agents: map[string]config.AgentConfig{"x": {Type: "http"}}}); err == nil {
		t.Fatal("http agent without endpoint should fail")
	}
	if err := validateConfig(&config.Config{RateLimitPerMinute: -1}); err == nil {
		t.Fatal("negative rate limit should fail")
	}
}

func TestValidateConfigRejectsLegacyClaudeCLI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "cli", Command: "claude"}
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "weclaw config agent") {
		t.Fatalf("validateConfig error=%v, want migration hint", err)
	}
}

func TestAgentStatusesExposeClaudeLocalHandoff(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "acp", Command: "claude-agent-acp", LocalCommand: "claude"}
	statuses := agentStatuses(cfg)
	if len(statuses) != 1 || statuses[0].LocalCommand != "claude" {
		t.Fatalf("statuses=%+v, want Claude local handoff", statuses)
	}
}
