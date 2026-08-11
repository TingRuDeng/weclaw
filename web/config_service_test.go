package web

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		"codex": {Type: "acp", Command: "codex", APIKey: "keep-key", Env: map[string]string{"K": "keep-val"}, PermissionLevel: "auto_review", ApprovalPolicy: "on-request", ApprovalReviewer: "auto_review", SandboxMode: "workspace-write", AppServerSocket: "/run/user/1000/weclaw/codex.sock", CodexHostMode: "managed", CodexAutoUpdate: "incompatible"},
	}}
	view := redactConfig(current)
	agentView := view.Agents["codex"]
	agentView.Command = "codex-2"
	view.Agents["codex"] = agentView
	merged := mergeView(current, view)
	agentCfg := merged.Agents["codex"]
	if merged.APIToken != "keep-token" || agentCfg.APIKey != "keep-key" || agentCfg.Env["K"] != "keep-val" {
		t.Fatalf("masked secrets must be preserved: %+v", agentCfg)
	}
	if agentCfg.PermissionLevel != "auto_review" || agentCfg.ApprovalPolicy != "on-request" || agentCfg.ApprovalReviewer != "auto_review" || agentCfg.SandboxMode != "workspace-write" {
		t.Fatalf("permission fields must be preserved: %+v", agentCfg)
	}
	if agentCfg.AppServerSocket != "/run/user/1000/weclaw/codex.sock" {
		t.Fatalf("app_server_socket must be preserved: %+v", agentCfg)
	}
	if agentCfg.CodexHostMode != "managed" {
		t.Fatalf("codex_host_mode must be preserved: %+v", agentCfg)
	}
	if agentCfg.CodexAutoUpdate != "incompatible" {
		t.Fatalf("codex_auto_update must be preserved: %+v", agentCfg)
	}
	if agentCfg.Command != "codex-2" {
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

func TestWebViewHidesAndPreservesLegacyAdminUsers(t *testing.T) {
	current := &config.Config{LegacyAdminUsers: []string{"old_admin"}}
	view := redactConfig(current)
	blob, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "admin_users") || strings.Contains(string(blob), "old_admin") {
		t.Fatalf("web view exposed legacy admin_users: %s", blob)
	}
	if got := mergeView(current, view).LegacyAdminUsers; !reflect.DeepEqual(got, []string{"old_admin"}) {
		t.Fatalf("LegacyAdminUsers=%#v, want preserved old_admin", got)
	}
}

func TestConfigServiceRejectsStaleViewWithoutRestoringRevokedAccess(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	base := config.DefaultConfig()
	base.Platforms["feishu"] = config.PlatformConfig{Bots: []config.FeishuBotConfig{{
		Name: "main", AppID: "cli_a", AllowedUsers: []string{"ou_user"},
	}}}
	if err := config.Save(base); err != nil {
		t.Fatal(err)
	}
	service := newConfigService()
	stale, err := service.view()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Update(func(cfg *config.Config) error {
		platformCfg := cfg.Platforms["feishu"]
		platformCfg.Bots[0].AllowedUsers = nil
		cfg.Platforms["feishu"] = platformCfg
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale.UpdateSource = "gitee"
	if _, err := service.apply(stale); err == nil || !strings.Contains(err.Error(), "配置已变化") {
		t.Fatalf("stale apply error=%v, want explicit conflict", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Platforms["feishu"].Bots[0].AllowedUsers; len(got) != 0 {
		t.Fatalf("stale web view restored revoked allowed_users: %#v", got)
	}
	if loaded.UpdateSource != "auto" {
		t.Fatalf("stale web view partially updated config: source=%q", loaded.UpdateSource)
	}
}

func TestUpdateSourceRoundTripsWithoutRestart(t *testing.T) {
	current := config.DefaultConfig()
	view := redactConfig(current)
	view.UpdateSource = "gitee"
	merged := mergeView(current, view)

	if merged.UpdateSource != "gitee" {
		t.Fatalf("UpdateSource=%q, want gitee", merged.UpdateSource)
	}
	if restartRequiredConfigChanged(current, merged) {
		t.Fatal("update_source change must not require service restart")
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
	statuses, err := platformStatuses(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, firstOK := findPlatformStatus(statuses, "feishu/project-a")
	second, secondOK := findPlatformStatus(statuses, "feishu/project-b")
	if !firstOK || !first.CredentialsPresent || first.AllowedUsersCount != 1 || !secondOK || second.CredentialsPresent || second.AllowedUsersCount != 0 {
		t.Fatalf("platform statuses=%#v", statuses)
	}
}

func TestPlatformStatusesPropagateWeChatCredentialReadFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "accounts"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := platformStatuses(config.DefaultConfig()); err == nil ||
		!strings.Contains(err.Error(), "WeChat credential status") {
		t.Fatalf("platformStatuses() error=%v, want WeChat credential read failure", err)
	}
}

func TestFeishuPlatformStatusesDistinguishMissingAndMalformedCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	enabled := true
	cfg := config.PlatformConfig{
		Enabled: &enabled,
		Bots:    []config.FeishuBotConfig{{Name: "project-a", AppID: "cli_a"}},
	}

	statuses, err := feishuPlatformStatuses(cfg)
	if err != nil || len(statuses) != 1 || statuses[0].CredentialsPresent {
		t.Fatalf("missing credentials statuses=%#v error=%v", statuses, err)
	}

	path, err := feishu.CredentialsPathForBot("project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := feishuPlatformStatuses(cfg); err == nil ||
		!strings.Contains(err.Error(), "project-a") {
		t.Fatalf("feishuPlatformStatuses() error=%v, want malformed credential failure", err)
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
