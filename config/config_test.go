package config

import (
	"encoding/json"
	"reflect"
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
	threadIsolation := false
	data := []byte(`{
		"platforms": {
			"feishu": {
				"enabled": true,
				"allowed_users": ["ou_1"],
				"default_agent": "codex",
				"progress": {"mode": "stream"},
				"message_aggregation_ms": 0,
				"require_mention_in_group": false,
				"thread_isolation": false
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
	if feishu.ThreadIsolation == nil || *feishu.ThreadIsolation != threadIsolation {
		t.Fatalf("ThreadIsolation=%#v, want explicit false", feishu.ThreadIsolation)
	}
}

func TestPlatformConfigDefaultsFeishuSessionRules(t *testing.T) {
	cfg := PlatformConfig{}

	if !cfg.EffectiveRequireMentionInGroup() {
		t.Fatal("EffectiveRequireMentionInGroup=false, want default true")
	}
	if !cfg.EffectiveThreadIsolation() {
		t.Fatal("EffectiveThreadIsolation=false, want default true")
	}
}

func TestDefaultProgressConfig(t *testing.T) {
	cfg := DefaultProgressConfig()

	if cfg.Mode != "typing" {
		t.Fatalf("Mode = %q, want typing", cfg.Mode)
	}
	if cfg.SendAcceptance == nil || *cfg.SendAcceptance {
		t.Fatalf("SendAcceptance = %#v, want false pointer", cfg.SendAcceptance)
	}
	if cfg.EnableTyping == nil || !*cfg.EnableTyping {
		t.Fatalf("EnableTyping = %#v, want true pointer", cfg.EnableTyping)
	}
	if cfg.ShowTextPreview == nil || *cfg.ShowTextPreview {
		t.Fatalf("ShowTextPreview = %#v, want false pointer", cfg.ShowTextPreview)
	}
	if cfg.SummaryIntervalSeconds != 20 {
		t.Fatalf("SummaryIntervalSeconds = %d, want 20", cfg.SummaryIntervalSeconds)
	}
	if cfg.MaxProgressMessages != 4 {
		t.Fatalf("MaxProgressMessages = %d, want 4", cfg.MaxProgressMessages)
	}
}

func TestProgressConfigUnmarshalDefaults(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"progress": {},
		"agents": {}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	cfg.Progress = NormalizeProgressConfig(DefaultProgressConfig(), &cfg.Progress)
	if cfg.Progress.Mode != "typing" {
		t.Fatalf("Mode = %q, want typing", cfg.Progress.Mode)
	}
	if cfg.Progress.SendAcceptance == nil || *cfg.Progress.SendAcceptance {
		t.Fatalf("SendAcceptance = %#v, want false pointer", cfg.Progress.SendAcceptance)
	}
}

func TestAgentProgressOverride(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"progress": {
			"mode": "summary"
		},
		"agents": {
			"codex": {
				"type": "acp",
				"progress": {
					"mode": "stream"
				}
			}
		}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	global := NormalizeProgressConfig(DefaultProgressConfig(), &cfg.Progress)
	agentCfg := NormalizeProgressConfig(global, cfg.Agents["codex"].Progress)
	if global.Mode != "summary" {
		t.Fatalf("global Mode = %q, want summary", global.Mode)
	}
	if agentCfg.Mode != "stream" {
		t.Fatalf("agent Mode = %q, want stream", agentCfg.Mode)
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

func TestLoadEnvOverridesProgressMode(t *testing.T) {
	t.Setenv("WECLAW_PROGRESS_MODE", "typing")

	cfg := DefaultConfig()
	loadEnv(cfg)

	if cfg.Progress.Mode != "typing" {
		t.Fatalf("Progress.Mode = %q, want typing", cfg.Progress.Mode)
	}
}
