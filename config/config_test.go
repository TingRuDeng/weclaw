package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAgentConfigUnmarshalEnv(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"agents": {
			"claude": {
				"type": "cli",
				"command": "claude",
				"env": {
					"ANTHROPIC_API_KEY": "test-key",
					"EMPTY": ""
				}
			}
		}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	ag, ok := cfg.Agents["claude"]
	if !ok {
		t.Fatalf("expected claude agent config")
	}
	if got := ag.Env["ANTHROPIC_API_KEY"]; got != "test-key" {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want %q", got, "test-key")
	}
	if got, ok := ag.Env["EMPTY"]; !ok || got != "" {
		t.Fatalf("EMPTY = %q, present=%v; want empty string present", got, ok)
	}
}

func TestAgentConfigMarshalEnvRoundTrip(t *testing.T) {
	cfg := Config{
		Agents: map[string]AgentConfig{
			"claude": {
				Type:    "cli",
				Command: "claude",
				Env: map[string]string{
					"ANTHROPIC_API_KEY": "test-key",
					"EMPTY":             "",
				},
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}

	got := decoded.Agents["claude"].Env
	if got["ANTHROPIC_API_KEY"] != "test-key" || got["EMPTY"] != "" {
		t.Fatalf("round-trip env = %#v", got)
	}
}

func TestAgentConfigWithoutEnvStillLoads(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"agents": {
			"claude": {
				"type": "cli",
				"command": "claude"
			}
		}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config without env: %v", err)
	}

	if cfg.Agents["claude"].Env != nil {
		t.Fatalf("Env = %#v, want nil", cfg.Agents["claude"].Env)
	}
}

func TestAgentConfigUnmarshalAutoLaunch(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"agents": {
			"codex": {
				"type": "companion",
				"command": "codex",
				"auto_launch": false
			}
		}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	got := cfg.Agents["codex"].AutoLaunch
	if got == nil || *got {
		t.Fatalf("AutoLaunch = %#v, want false pointer", got)
	}
}

func TestNormalizeCodexRemoteFirstMigratesCompanion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents["codex"] = AgentConfig{
		Type:    "companion",
		Command: "codex",
		Args:    []string{"-c", "model=\"gpt-test\""},
		Cwd:     "/tmp/work",
	}

	if !NormalizeCodexRemoteFirst(cfg) {
		t.Fatal("NormalizeCodexRemoteFirst() = false, want true")
	}
	got := cfg.Agents["codex"]
	if got.Type != "acp" {
		t.Fatalf("Type=%q, want acp", got.Type)
	}
	wantArgs := []string{"-c", "model=\"gpt-test\"", "app-server", "--listen", "stdio://"}
	if !reflect.DeepEqual(got.Args, wantArgs) {
		t.Fatalf("Args=%#v, want %#v", got.Args, wantArgs)
	}
	if got.AutoLaunch != nil {
		t.Fatalf("AutoLaunch=%#v, want nil", got.AutoLaunch)
	}
}

func TestDefaultConfigInitializesAgentsMap(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Agents == nil {
		t.Fatal("DefaultConfig() Agents = nil, want initialized map")
	}
	if cfg.Platforms == nil {
		t.Fatal("DefaultConfig() Platforms = nil, want initialized map")
	}
}

func TestPlatformConfigUnmarshal(t *testing.T) {
	var cfg Config
	requireMention := false
	data := []byte(`{
		"platforms": {
			"feishu": {
				"enabled": true,
				"allowed_users": ["ou_1"],
				"default_agent": "codex",
				"progress": {"mode": "stream"},
				"message_aggregation_ms": 0,
				"require_mention_in_group": false
			}
		},
		"agents": {}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	feishu := cfg.Platforms["feishu"]
	if feishu.Enabled == nil || !*feishu.Enabled {
		t.Fatalf("feishu.Enabled=%#v, want true", feishu.Enabled)
	}
	if !reflect.DeepEqual(feishu.AllowedUsers, []string{"ou_1"}) {
		t.Fatalf("AllowedUsers=%#v, want ou_1", feishu.AllowedUsers)
	}
	if feishu.DefaultAgent != "codex" {
		t.Fatalf("DefaultAgent=%q, want codex", feishu.DefaultAgent)
	}
	if feishu.Progress == nil || feishu.Progress.Mode != "stream" {
		t.Fatalf("Progress=%#v, want stream", feishu.Progress)
	}
	if feishu.MessageAggregationMs == nil || *feishu.MessageAggregationMs != 0 {
		t.Fatalf("MessageAggregationMs=%#v, want explicit 0", feishu.MessageAggregationMs)
	}
	if feishu.RequireMentionInGroup == nil || *feishu.RequireMentionInGroup != requireMention {
		t.Fatalf("RequireMentionInGroup=%#v, want explicit false", feishu.RequireMentionInGroup)
	}
}

func TestPlatformConfigUnmarshalFeishuBots(t *testing.T) {
	var cfg Config
	requireMention := false
	data := []byte(`{
		"platforms": {
			"feishu": {
				"enabled": true,
				"bots": [
					{
						"name": "project-a",
						"app_id": "cli_a",
						"allowed_users": ["ou_1"],
						"default_agent": "codex",
						"progress": {"mode": "stream"},
						"require_mention_in_group": false
					},
					{
						"name": "project-b",
						"app_id": "cli_b",
						"allowed_users": ["ou_2"]
					}
				]
			}
		},
		"agents": {}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	bots := cfg.Platforms["feishu"].Bots
	if len(bots) != 2 {
		t.Fatalf("Bots=%#v, want two bots", bots)
	}
	if bots[0].Name != "project-a" || bots[0].AppID != "cli_a" {
		t.Fatalf("first bot=%#v, want project-a/cli_a", bots[0])
	}
	if !reflect.DeepEqual(bots[0].AllowedUsers, []string{"ou_1"}) {
		t.Fatalf("AllowedUsers=%#v, want ou_1", bots[0].AllowedUsers)
	}
	if bots[0].DefaultAgent != "codex" {
		t.Fatalf("DefaultAgent=%q, want codex", bots[0].DefaultAgent)
	}
	if bots[0].Progress == nil || bots[0].Progress.Mode != "stream" {
		t.Fatalf("Progress=%#v, want stream", bots[0].Progress)
	}
	if bots[0].RequireMentionInGroup == nil || *bots[0].RequireMentionInGroup != requireMention {
		t.Fatalf("RequireMentionInGroup=%#v, want explicit false", bots[0].RequireMentionInGroup)
	}
}

func TestValidateFeishuBotsRejectsDuplicateName(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Platforms["feishu"] = PlatformConfig{Bots: []FeishuBotConfig{
		{Name: "project-a", AppID: "cli_a"},
		{Name: "project-a", AppID: "cli_b"},
	}}

	err := cfg.Validate()

	if err == nil || !strings.Contains(err.Error(), "duplicate feishu bot name") {
		t.Fatalf("Validate error=%v, want duplicate bot name", err)
	}
}

func TestValidateFeishuBotsRejectsDuplicateAlias(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Platforms["feishu"] = PlatformConfig{Bots: []FeishuBotConfig{
		{Name: "project-a", AppID: "cli_a", DisplayName: "卡片管家"},
		{Name: "project-b", AppID: "cli_b", Aliases: []string{"卡片管家"}},
	}}

	err := cfg.Validate()

	if err == nil || !strings.Contains(err.Error(), `duplicate feishu bot alias "卡片管家"`) {
		t.Fatalf("Validate error=%v, want duplicate bot alias", err)
	}
}

func TestValidateFeishuBotsRejectsLegacySingleBotConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Platforms["feishu"] = PlatformConfig{AllowedUsers: []string{"ou_1"}}

	err := cfg.Validate()

	if err == nil || !strings.Contains(err.Error(), "platforms.feishu.bots") {
		t.Fatalf("Validate error=%v, want legacy config rejection", err)
	}
}

func TestValidateFeishuRejectsEnabledWithoutBots(t *testing.T) {
	enabled := true
	cfg := DefaultConfig()
	cfg.Platforms["feishu"] = PlatformConfig{Enabled: &enabled}

	err := cfg.Validate()

	if err == nil || !strings.Contains(err.Error(), "platforms.feishu.bots is required") {
		t.Fatalf("Validate error=%v, want missing bots rejection", err)
	}
}

func TestPlatformConfigDefaultsFeishuSessionRules(t *testing.T) {
	cfg := PlatformConfig{}

	if !cfg.EffectiveRequireMentionInGroup() {
		t.Fatal("EffectiveRequireMentionInGroup=false, want default true")
	}
}

func TestConfigUnmarshalAdminUsers(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"admin_users": ["ou_admin", "wx_admin"],
		"agents": {}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	want := []string{"ou_admin", "wx_admin"}
	if !reflect.DeepEqual(cfg.AdminUsers, want) {
		t.Fatalf("AdminUsers=%#v, want %#v", cfg.AdminUsers, want)
	}
}

func TestLoadEnvOverridesTopLevelOnly(t *testing.T) {
	t.Setenv("WECLAW_DEFAULT_AGENT", "codex")
	t.Setenv("WECLAW_API_ADDR", "127.0.0.1:18011")
	t.Setenv("WECLAW_API_TOKEN", "secret-token")

	cfg := DefaultConfig()
	cfg.Agents["claude"] = AgentConfig{
		Type: "cli",
		Env: map[string]string{
			"KEEP": "value",
		},
	}

	loadEnv(cfg)

	if cfg.DefaultAgent != "codex" {
		t.Fatalf("DefaultAgent = %q, want %q", cfg.DefaultAgent, "codex")
	}
	if cfg.APIAddr != "127.0.0.1:18011" {
		t.Fatalf("APIAddr = %q, want %q", cfg.APIAddr, "127.0.0.1:18011")
	}
	if cfg.APIToken != "secret-token" {
		t.Fatalf("APIToken = %q, want %q", cfg.APIToken, "secret-token")
	}
	if got := cfg.Agents["claude"].Env["KEEP"]; got != "value" {
		t.Fatalf("agent env = %q, want preserved value", got)
	}
}
