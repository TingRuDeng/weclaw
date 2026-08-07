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
	if cfg.StreamTimelineLimit == nil || *cfg.StreamTimelineLimit != 0 {
		t.Fatalf("StreamTimelineLimit = %#v, want pointer to 0", cfg.StreamTimelineLimit)
	}
}

func TestProgressConfigMissingStreamTimelineLimitIsUnlimited(t *testing.T) {
	if got := (ProgressConfig{}).EffectiveStreamTimelineLimit(); got != 0 {
		t.Fatalf("EffectiveStreamTimelineLimit() = %d, want 0", got)
	}
}

func TestProgressConfigStreamTimelineLimitExplicitZeroOverridesParent(t *testing.T) {
	base := DefaultProgressConfig()
	zero := 0
	got := NormalizeProgressConfig(base, &ProgressConfig{StreamTimelineLimit: &zero})
	if got.StreamTimelineLimit == nil || *got.StreamTimelineLimit != 0 {
		t.Fatalf("StreamTimelineLimit = %#v, want explicit zero", got.StreamTimelineLimit)
	}
}

func TestProgressConfigStreamTimelineLimitPositiveOverridesParent(t *testing.T) {
	base := DefaultProgressConfig()
	limit := 24
	got := NormalizeProgressConfig(base, &ProgressConfig{StreamTimelineLimit: &limit})
	if got.StreamTimelineLimit == nil || *got.StreamTimelineLimit != limit {
		t.Fatalf("StreamTimelineLimit = %#v, want %d", got.StreamTimelineLimit, limit)
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

func TestLoadEnvAllowsUnlimitedStreamTimeline(t *testing.T) {
	t.Setenv("WECLAW_PROGRESS_STREAM_TIMELINE_LIMIT", "0")
	cfg := DefaultConfig()
	loadEnv(cfg)
	if cfg.Progress.StreamTimelineLimit == nil || *cfg.Progress.StreamTimelineLimit != 0 {
		t.Fatalf("StreamTimelineLimit=%#v, want explicit zero", cfg.Progress.StreamTimelineLimit)
	}
}
