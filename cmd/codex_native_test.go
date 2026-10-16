package cmd

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
)

func TestNativeCLIIsIndependentOfWeClawRestartAndService(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	lease, err := agent.AcquireCodexRestartLease()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	oldLoad, oldPrepare, oldExecute, oldService := loadCodexCLIConfig, prepareCodexCLI, executeCodexCLI, prepareCodexCLIHostWithService
	t.Cleanup(func() {
		loadCodexCLIConfig, prepareCodexCLI, executeCodexCLI, prepareCodexCLIHostWithService = oldLoad, oldPrepare, oldExecute, oldService
	})
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex", Args: []string{"app-server"}}
	loadCodexCLIConfig = func() (*config.Config, error) { return cfg, nil }
	prepareCodexCLIHostWithService = func(context.Context, *config.Config) (agent.CodexCLIHost, error) {
		t.Fatal("native CLI must not depend on WeClaw's API")
		return agent.CodexCLIHost{}, nil
	}
	prepareCodexCLI = func(_ context.Context, _ agent.ACPAgentConfig, opts agent.CodexCLILaunchOptions) (agent.CodexCLILaunch, error) {
		if !opts.AllowHostStart {
			t.Fatal("native CLI must use official lifecycle independently")
		}
		return agent.CodexCLILaunch{Command: "codex"}, nil
	}
	executed := false
	executeCodexCLI = func(context.Context, agent.CodexCLILaunch) error { executed = true; return nil }
	if err := runCodexCLI(context.Background(), []string{"resume", "thread-1"}); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("native CLI did not start")
	}
}

func TestNativeServiceRestartDoesNotStartUnavailableCodex(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	h := messaging.NewHandlerWithErrorFactory(func(context.Context, string) (agent.Agent, error) {
		t.Fatal("ordinary service restart must not start the external Codex runtime")
		return nil, nil
	}, nil)
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex", Args: []string{"app-server"}}
	configureHandlerMetadata(h, cfg)
	result, err := h.PrepareRuntimeRestart(context.Background(), false)
	if err != nil || result.Codex || result.CodexHost.HostStopped {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := h.CancelRuntimeRestart(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCodexConfigDoesNotInstallDesktopCoordination(t *testing.T) {
	enabled := true
	cfg := config.AgentConfig{Type: "acp", Command: "codex", Args: []string{"app-server"},
		CodexMultiFrontend: &enabled, CodexAppDaemon: &enabled}
	got := acpAgentConfigFromConfig("codex", cfg)
	if got.CodexDesktopBridge || (got.CodexAppDaemon != nil && *got.CodexAppDaemon) {
		t.Fatal("legacy hints re-enabled App startup coordination")
	}
	if shouldWarmCodexAppDaemonReuse(&config.Config{Agents: map[string]config.AgentConfig{"codex": cfg}}) {
		t.Fatal("native Codex must not prewarm App integration")
	}
}
