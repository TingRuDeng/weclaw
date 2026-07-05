package config

import (
	"encoding/json"
	"testing"
)

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

func TestLoadEnvOverridesProgressMode(t *testing.T) {
	t.Setenv("WECLAW_PROGRESS_MODE", "typing")

	cfg := DefaultConfig()
	loadEnv(cfg)

	if cfg.Progress.Mode != "typing" {
		t.Fatalf("Progress.Mode = %q, want typing", cfg.Progress.Mode)
	}
}
