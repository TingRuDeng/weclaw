package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

func TestCreateAgentByNameRejectsClaudeCLI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{
		Type: "cli", Command: "claude", Model: "opus", Effort: "high", Cwd: t.TempDir(),
	}

	if ag := createAgentByName(context.Background(), cfg, "claude"); ag != nil {
		t.Fatalf("agent=%T，Claude CLI 后端必须被拒绝", ag)
	}
}

func TestACPAgentConfigFromConfigPassesCodexHostMode(t *testing.T) {
	reuseDaemon := true
	got := acpAgentConfigFromConfig("codex", config.AgentConfig{
		Type: "acp", Command: "codex", Args: []string{"app-server"},
		CodexHostMode: "daemon", CodexAppDaemon: &reuseDaemon,
	})
	if got.CodexHostMode != "daemon" {
		t.Fatalf("CodexHostMode=%q, want daemon", got.CodexHostMode)
	}
	if got.CodexDesktopBridge != (runtime.GOOS == "darwin") {
		t.Fatalf("daemon CodexDesktopBridge=%v, want Darwin-only coordination", got.CodexDesktopBridge)
	}
	if got.CodexAppDaemon == nil || !*got.CodexAppDaemon {
		t.Fatalf("CodexAppDaemon=%v, want true", got.CodexAppDaemon)
	}
	if defaulted := acpAgentConfigFromConfig("codex", config.AgentConfig{}).CodexHostMode; defaulted != "auto" {
		t.Fatalf("default CodexHostMode=%q, want auto", defaulted)
	}
	auto := acpAgentConfigFromConfig("codex", config.AgentConfig{
		Type: "acp", Command: "codex", Args: []string{"app-server"},
	})
	if auto.CodexDesktopBridge != (runtime.GOOS == "darwin") {
		t.Fatalf("CodexDesktopBridge=%v, want Darwin-only auto bridge", auto.CodexDesktopBridge)
	}
	custom := acpAgentConfigFromConfig("codex", config.AgentConfig{
		Type: "acp", Command: "codex", Args: []string{"app-server"},
		AppServerSocket: "/tmp/codex.sock",
	})
	if custom.CodexDesktopBridge {
		t.Fatal("custom app-server socket must keep the explicitly selected shared Host")
	}
}

func TestShouldWarmCodexAppDaemonReuse(t *testing.T) {
	enabled := true
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{
		Type: "acp", Command: "codex", Args: []string{"app-server"},
		CodexHostMode: "auto", CodexAppDaemon: &enabled,
	}
	if got, want := shouldWarmCodexAppDaemonReuse(cfg), runtime.GOOS == "darwin"; got != want {
		t.Fatalf("shouldWarmCodexAppDaemonReuse()=%v, want %v", got, want)
	}
	codex := cfg.Agents["codex"]
	codex.CodexHostMode = "managed"
	cfg.Agents["codex"] = codex
	if shouldWarmCodexAppDaemonReuse(cfg) {
		t.Fatal("managed Host must not prewarm App daemon reuse")
	}
}

func TestCreateAgentByNamePassesACPConfiguredName(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	t.Setenv("WECLAW_TEST_ACP_CONFIGURED_NAME", "1")
	cfg := config.DefaultConfig()
	cfg.Agents["claude"] = config.AgentConfig{
		Type: "acp", Command: os.Args[0],
		Args: []string{"-test.run=TestHelperACPConfiguredName"}, Cwd: t.TempDir(),
	}

	if ag := createAgentByName(context.Background(), cfg, "claude"); ag != nil {
		if stopper, ok := ag.(interface{ Stop() }); ok {
			stopper.Stop()
		}
		t.Fatalf("createAgentByName()=%T, want Claude capability gate", ag)
	}
}

// TestHelperACPConfiguredName 返回缺少 list/resume 和 agentInfo 的合法握手。
func TestHelperACPConfiguredName(t *testing.T) {
	if os.Getenv("WECLAW_TEST_ACP_CONFIGURED_NAME") != "1" {
		return
	}
	line, err := bufio.NewReader(os.Stdin).ReadBytes('\n')
	if err != nil {
		os.Exit(2)
	}
	var request struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(line, &request) != nil {
		os.Exit(3)
	}
	response := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":1,"agentCapabilities":{}}}`+"\n", request.ID)
	if _, err := io.WriteString(os.Stdout, response); err != nil {
		os.Exit(4)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestCreateAgentByNameRetriesCodexSQLiteRuntimeStartup(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	oldDelay := codexACPStartupRetryDelay
	oldStart := startACPAgentCall
	codexACPStartupRetryDelay = time.Millisecond
	attempts := 0
	startACPAgentCall = func(context.Context, *agent.ACPAgent) error {
		attempts++
		if attempts < 3 {
			return errors.New("failed to initialize sqlite state runtime under test CODEX_HOME")
		}
		return nil
	}
	t.Cleanup(func() {
		codexACPStartupRetryDelay = oldDelay
		startACPAgentCall = oldStart
	})

	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{
		Type:          "acp",
		Command:       "codex",
		Args:          []string{"app-server", "--listen", "stdio://"},
		Cwd:           t.TempDir(),
		CodexHostMode: "managed",
	}

	ag := createAgentByName(context.Background(), cfg, "codex")
	if ag == nil {
		t.Fatal("createAgentByName() = nil, want agent after retry")
	}
	t.Cleanup(func() {
		if stopper, ok := ag.(interface{ Stop() }); ok {
			stopper.Stop()
		}
	})
	if attempts != 3 {
		t.Fatalf("attempts=%d, want 3", attempts)
	}
}

func TestCompanionAutoLaunchDefaultsToRemoteOnly(t *testing.T) {
	cfg := config.AgentConfig{Type: "companion"}
	if companionAutoLaunchEnabled("codex", cfg) {
		t.Fatal("codex companion should not auto launch by default")
	}
	if companionAutoLaunchEnabled("opencode", cfg) {
		t.Fatal("opencode companion should not auto launch by default")
	}
	enabled := true
	cfg.AutoLaunch = &enabled
	if companionAutoLaunchEnabled("codex", cfg) {
		return
	}
	t.Fatal("explicit true should enable codex auto launch")
}

func TestCreateAgentByNameCreatesCompanionAgent(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents["opencode"] = config.AgentConfig{
		Type:    "companion",
		Command: "opencode",
		Cwd:     workspace,
	}

	ag := createAgentByName(context.Background(), cfg, "opencode")
	if ag == nil {
		t.Fatal("createAgentByName() = nil, want companion agent")
	}
	t.Cleanup(func() {
		if stopper, ok := ag.(interface{ Stop() }); ok {
			stopper.Stop()
		}
	})
	info := ag.Info()
	if info.Type != "companion" || info.Name != "opencode" {
		t.Fatalf("Info() = %#v, want opencode companion", info)
	}
}

func TestCreateAgentByNameRejectsUnknownCompanionCommand(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents["opencode"] = config.AgentConfig{
		Type: "companion",
		Cwd:  t.TempDir(),
	}

	ag := createAgentByName(context.Background(), cfg, "opencode")
	if ag != nil {
		t.Fatalf("createAgentByName() = %#v, want nil without command", ag)
	}
}
