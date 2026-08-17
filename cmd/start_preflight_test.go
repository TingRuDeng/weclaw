package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestStartPreflightRejectsMissingMultiFrontendStandalone(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	codexHome := t.TempDir()
	var cfg config.Config
	data := []byte(`{
		"default_agent": "codex",
		"agents": {
			"codex": {
				"type": "acp",
				"command": "codex",
				"args": ["app-server", "--listen", "stdio://"],
				"env": {"CODEX_HOME": "` + codexHome + `"},
				"codex_auto_update": "incompatible",
				"codex_app_reuse_daemon": true,
				"codex_multi_frontend": true
			}
		}
	}`)
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}

	err := preflightStartConfig(context.Background(), &cfg)
	if err == nil || !strings.Contains(err.Error(), "codex_multi_frontend") || !strings.Contains(err.Error(), "doctor --fix --components codex") {
		t.Fatalf("preflightStartConfig() error=%v, want blocking standalone install guidance", err)
	}
}

// TestPrepareConfiguredStartDaemonChildSkipsCapabilityProbe 验证后台子进程不重复执行父进程已完成的 ACP 握手。
func TestPrepareConfiguredStartDaemonChildSkipsCapabilityProbe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	t.Setenv(daemonChildEnv, "1")
	t.Setenv("PATH", t.TempDir())
	marker := filepath.Join(home, "adapter-started")
	adapter := filepath.Join(home, "claude-agent-acp")
	script := "#!/bin/sh\n/usr/bin/touch '" + marker + "'\nexit 31\n"
	if err := os.WriteFile(adapter, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "acp", Command: adapter}
	fingerprint, err := claudePreflightFingerprint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(daemonClaudePreflightEnv, fingerprint)
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareConfiguredStart(context.Background(), func(*config.Config) error { return nil })
	if err != nil {
		t.Fatalf("prepareConfiguredStart error=%v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("daemon 子进程重复执行了 ACP adapter，stat error=%v", err)
	}
	if err := prepared.run(); err != nil {
		t.Fatalf("prepared.run error=%v", err)
	}
}

func TestDaemonChildRejectsClaudeConfigChangedAfterParentPreflight(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	t.Setenv(daemonChildEnv, "1")
	t.Setenv("PATH", t.TempDir())
	adapterA := filepath.Join(home, "claude-agent-acp-a")
	adapterB := filepath.Join(home, "claude-agent-acp-b")
	for _, path := range []string{adapterA, adapterB} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	parent := config.DefaultConfig()
	parent.Agents["claude"] = config.AgentConfig{Type: "acp", Command: adapterA, Model: "sonnet"}
	fingerprint, err := claudePreflightFingerprint(parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(daemonClaudePreflightEnv, fingerprint)
	changed := config.DefaultConfig()
	changed.Agents["claude"] = config.AgentConfig{Type: "acp", Command: adapterB, Model: "opus"}
	if err := config.Save(changed); err != nil {
		t.Fatal(err)
	}

	if _, err := prepareConfiguredStart(context.Background(), func(*config.Config) error { return nil }); err == nil {
		t.Fatal("daemon child accepted Claude config that was not attested by the parent preflight")
	}
}

func TestDaemonChildEnvironmentCarriesClaudePreflightFingerprint(t *testing.T) {
	env := daemonChildEnvironment([]string{
		"PATH=/usr/bin",
		daemonChildEnv + "=stale",
		daemonClaudePreflightEnv + "=stale",
	}, "sha256:test")
	if got := environmentValue(env, daemonChildEnv); got != "1" {
		t.Fatalf("daemon child marker=%q, want 1", got)
	}
	if got := environmentValue(env, daemonClaudePreflightEnv); got != "sha256:test" {
		t.Fatalf("daemon Claude preflight fingerprint=%q", got)
	}
}

func environmentValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

// TestConfiguredStartPreflightParentKeepsCapabilityProbe 验证父进程仍执行完整 ACP 能力握手。
func TestConfiguredStartPreflightParentKeepsCapabilityProbe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	t.Setenv(daemonChildEnv, "")
	t.Setenv("PATH", t.TempDir())
	marker := filepath.Join(home, "adapter-started")
	adapter := filepath.Join(home, "claude-agent-acp")
	script := "#!/bin/sh\n/usr/bin/touch '" + marker + "'\nexit 31\n"
	if err := os.WriteFile(adapter, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{Type: "acp", Command: adapter}
	err := configuredStartPreflight()(context.Background(), cfg)
	if err == nil {
		t.Fatal("父进程必须暴露 ACP 能力握手失败")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("父进程未执行 ACP adapter，stat error=%v", err)
	}
}

// TestPrepareStartRejectsLoadAndPreflightErrors 验证启动准备阶段不会把错误延迟到停服之后。
func TestPrepareStartRejectsLoadAndPreflightErrors(t *testing.T) {
	wantLoadErr := errors.New("加载失败")
	_, err := prepareStart(context.Background(), startPreparationOps{
		loadConfig: func() (*config.Config, error) { return nil, wantLoadErr },
	})
	if !errors.Is(err, wantLoadErr) {
		t.Fatalf("load error=%v, want %v", err, wantLoadErr)
	}
	wantPreflightErr := errors.New("能力缺失")
	_, err = prepareStart(context.Background(), startPreparationOps{
		loadConfig: func() (*config.Config, error) { return config.DefaultConfig(), nil },
		preflight:  func(context.Context, *config.Config) error { return wantPreflightErr },
	})
	if !errors.Is(err, wantPreflightErr) {
		t.Fatalf("preflight error=%v, want %v", err, wantPreflightErr)
	}
}

// TestPersistDetectedStartConfigSkipsUnchangedConfig 验证无自动探测变更时不触碰配置文件。
func TestPersistDetectedStartConfigSkipsUnchangedConfig(t *testing.T) {
	called := false
	err := persistDetectedStartConfig(false, config.DefaultConfig(), func(func(*config.Config) error) error {
		called = true
		return nil
	})
	if err != nil || called {
		t.Fatalf("error=%v called=%t, want no save", err, called)
	}
}

// TestPersistDetectedStartConfigSavesChangedConfig 验证自动探测配置在启动前持久化。
func TestPersistDetectedStartConfigSavesChangedConfig(t *testing.T) {
	wantCfg := completeStartPreflightAgentConfig("/usr/bin/true")
	wantCfg.RateLimitPerMinute = 17
	candidate := completeStartPreflightAgentConfig("/usr/bin/true")
	called := false
	err := persistDetectedStartConfig(true, candidate, func(mutate func(*config.Config) error) error {
		called = true
		return mutate(wantCfg)
	})
	if err != nil || !called || candidate.RateLimitPerMinute != 17 {
		t.Fatalf("error=%v called=%t candidate=%#v, want latest committed config", err, called, candidate)
	}
}

func TestPersistDetectedStartConfigDoesNotRestoreConcurrentRevocation(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	base := completeStartPreflightAgentConfig("/usr/bin/true")
	base.Platforms["feishu"] = config.PlatformConfig{Bots: []config.FeishuBotConfig{{
		Name: "main", AppID: "cli_a", AllowedUsers: []string{"victim"},
	}}}
	if err := config.Save(base); err != nil {
		t.Fatal(err)
	}
	candidate, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Update(func(latest *config.Config) error {
		platformCfg := latest.Platforms["feishu"]
		platformCfg.Bots[0].AllowedUsers = nil
		latest.Platforms["feishu"] = platformCfg
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := persistDetectedStartConfig(true, candidate, config.Update); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Platforms["feishu"].Bots[0].AllowedUsers; len(got) != 0 {
		t.Fatalf("start preflight restored revoked allowed_users: %#v", got)
	}
}

func TestPersistDetectedStartConfigRejectsConcurrentClaudeAppearance(t *testing.T) {
	binDir := t.TempDir()
	adapter := filepath.Join(binDir, "claude-agent-acp")
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	candidate := completeStartPreflightAgentConfig(adapter)
	delete(candidate.Agents, "claude")
	latest := completeStartPreflightAgentConfig(adapter)

	err := persistDetectedStartConfig(true, candidate, func(mutate func(*config.Config) error) error {
		return mutate(latest)
	})
	if err == nil {
		t.Fatal("concurrently appeared Claude config bypassed the parent capability preflight")
	}
}

func TestDetectStartAgentsPreservesPreflightResolvedClaudeCommand(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	binDir := t.TempDir()
	adapter := filepath.Join(binDir, "claude-agent-acp")
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	cfg := completeStartPreflightAgentConfig("claude-agent-acp")
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	preflighted, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	claude := preflighted.Agents["claude"]
	claude.Command = adapter
	preflighted.Agents["claude"] = claude

	if err := detectStartAgents(preflighted); err != nil {
		t.Fatal(err)
	}
	if got := preflighted.Agents["claude"].Command; got != adapter {
		t.Fatalf("runtime Claude command=%q, want preflighted %q", got, adapter)
	}
}

func TestStartRejectsConcurrentUnprobedClaudeConfigChange(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	binDir := t.TempDir()
	adapter := filepath.Join(binDir, "claude-agent-acp")
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	cfg := completeStartPreflightAgentConfig("claude-agent-acp")
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	preflighted, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	claude := preflighted.Agents["claude"]
	claude.Command = adapter
	preflighted.Agents["claude"] = claude
	if err := config.Update(func(latest *config.Config) error {
		changed := latest.Agents["claude"]
		changed.Model = "opus"
		latest.Agents["claude"] = changed
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := detectStartAgents(preflighted); err == nil {
		t.Fatal("concurrently changed Claude config bypassed capability preflight")
	}
}

func completeStartPreflightAgentConfig(claudeCommand string) *config.Config {
	cfg := config.DefaultConfig()
	for _, name := range []string{
		"codex", "cursor", "kimi", "gemini", "opencode", "openclaw", "pi",
		"copilot", "droid", "iflow", "kiro", "qwen",
	} {
		cfg.Agents[name] = config.AgentConfig{Type: "http", Endpoint: "http://127.0.0.1"}
	}
	cfg.Agents["claude"] = config.AgentConfig{Type: "acp", Command: claudeCommand, Model: "sonnet"}
	return cfg
}
