package config

import (
	"encoding/json"
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

func TestDefaultConfigInitializesAgentsMap(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Agents == nil {
		t.Fatal("DefaultConfig() Agents = nil, want initialized map")
	}
}

func TestDefaultProgressConfig(t *testing.T) {
	cfg := DefaultProgressConfig()

	if cfg.Mode != "summary" {
		t.Fatalf("Mode = %q, want summary", cfg.Mode)
	}
	if cfg.SendAcceptance == nil || !*cfg.SendAcceptance {
		t.Fatalf("SendAcceptance = %#v, want true pointer", cfg.SendAcceptance)
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
	if cfg.Progress.Mode != "summary" {
		t.Fatalf("Mode = %q, want summary", cfg.Progress.Mode)
	}
	if cfg.Progress.SendAcceptance == nil || !*cfg.Progress.SendAcceptance {
		t.Fatalf("SendAcceptance = %#v, want true pointer", cfg.Progress.SendAcceptance)
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
