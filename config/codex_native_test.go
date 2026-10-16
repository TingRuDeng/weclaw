package config

import "testing"

func TestNativeCodexDefaultsDoNotEnableAppCoordination(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents["codex"] = AgentConfig{Type: "acp", Command: "codex", Args: []string{"app-server"}}
	NormalizeCodexRemoteFirst(cfg)
	got := cfg.Agents["codex"]
	if got.CodexMultiFrontend != nil || got.CodexAppDaemon != nil {
		t.Fatalf("default config must not persist App coordination: %+v", got)
	}
}

func TestNativeCodexIgnoresLegacyCoordinationHints(t *testing.T) {
	for _, mode := range []string{"", "auto", "native"} {
		t.Run(mode, func(t *testing.T) {
			enabled := true
			cfg := AgentConfig{Type: "acp", Command: "codex", Args: []string{"app-server"},
				CodexHostMode: mode, CodexMultiFrontend: &enabled, CodexAppDaemon: &enabled}
			if cfg.EffectiveCodexMultiFrontend() || cfg.EffectiveCodexAppDaemon() {
				t.Fatal("native client must not require App coordination from legacy hints")
			}
			if err := cfg.ValidateCodexHostModeConfig(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
