package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

func TestRunCodexCLIForwardsFrontendArguments(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	originalLoad := loadCodexCLIConfig
	originalGetwd := codexCLIGetwd
	originalPrepare := prepareCodexCLI
	originalExecute := executeCodexCLI
	originalServicePrepare := prepareCodexCLIHostWithService
	t.Cleanup(func() {
		loadCodexCLIConfig = originalLoad
		codexCLIGetwd = originalGetwd
		prepareCodexCLI = originalPrepare
		executeCodexCLI = originalExecute
		prepareCodexCLIHostWithService = originalServicePrepare
	})
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{
		Type: "acp", Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
	}
	loadCodexCLIConfig = func() (*config.Config, error) { return cfg, nil }
	codexCLIGetwd = func() (string, error) { return "/tmp/project", nil }
	var prepared agent.CodexCLILaunchOptions
	prepareCodexCLI = func(_ context.Context, runtimeConfig agent.ACPAgentConfig, opts agent.CodexCLILaunchOptions) (agent.CodexCLILaunch, error) {
		if runtimeConfig.Cwd != "/tmp/project" || runtimeConfig.Command != "codex" {
			t.Fatalf("runtime config=%#v", runtimeConfig)
		}
		prepared = opts
		return agent.CodexCLILaunch{Command: "/managed/codex", Args: []string{"--remote", "unix:///tmp/codex.sock"}, Cwd: opts.Cwd}, nil
	}
	var executed agent.CodexCLILaunch
	executeCodexCLI = func(_ context.Context, launch agent.CodexCLILaunch) error {
		executed = launch
		return nil
	}

	args := []string{"resume", "thread-1"}
	if err := runCodexCLI(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if !prepared.AllowHostStart || prepared.Cwd != "/tmp/project" || !reflect.DeepEqual(prepared.Args, args) {
		t.Fatalf("prepare options=%#v", prepared)
	}
	if executed.Command != "/managed/codex" || executed.Cwd != "/tmp/project" {
		t.Fatalf("executed=%#v", executed)
	}
}

func TestRunCodexCLIUsesRunningServiceToPrepareHost(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	if err := writeRuntimeState(runtimeState{PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	originalLoad := loadCodexCLIConfig
	originalGetwd := codexCLIGetwd
	originalPrepare := prepareCodexCLI
	originalExecute := executeCodexCLI
	originalServicePrepare := prepareCodexCLIHostWithService
	t.Cleanup(func() {
		loadCodexCLIConfig = originalLoad
		codexCLIGetwd = originalGetwd
		prepareCodexCLI = originalPrepare
		executeCodexCLI = originalExecute
		prepareCodexCLIHostWithService = originalServicePrepare
	})
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex", Args: []string{"app-server"}}
	loadCodexCLIConfig = func() (*config.Config, error) { return cfg, nil }
	codexCLIGetwd = func() (string, error) { return "/tmp/project", nil }
	prepareCodexCLIHostWithService = func(context.Context, *config.Config) (agent.CodexCLIHost, error) {
		return agent.CodexCLIHost{SocketPath: "/tmp/shared.sock"}, nil
	}
	prepareCodexCLI = func(_ context.Context, _ agent.ACPAgentConfig, opts agent.CodexCLILaunchOptions) (agent.CodexCLILaunch, error) {
		if opts.AllowHostStart {
			t.Fatal("live service must own Host startup")
		}
		return agent.CodexCLILaunch{Command: "/managed/codex", SocketPath: "/tmp/shared.sock"}, nil
	}
	executeCodexCLI = func(context.Context, agent.CodexCLILaunch) error { return nil }

	if err := runCodexCLI(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunCodexCLIRejectsRunningServiceSocketMismatch(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	if err := writeRuntimeState(runtimeState{PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	originalLoad := loadCodexCLIConfig
	originalGetwd := codexCLIGetwd
	originalPrepare := prepareCodexCLI
	originalExecute := executeCodexCLI
	originalServicePrepare := prepareCodexCLIHostWithService
	t.Cleanup(func() {
		loadCodexCLIConfig = originalLoad
		codexCLIGetwd = originalGetwd
		prepareCodexCLI = originalPrepare
		executeCodexCLI = originalExecute
		prepareCodexCLIHostWithService = originalServicePrepare
	})
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex", Args: []string{"app-server"}}
	loadCodexCLIConfig = func() (*config.Config, error) { return cfg, nil }
	codexCLIGetwd = func() (string, error) { return "/tmp/project", nil }
	prepareCodexCLIHostWithService = func(context.Context, *config.Config) (agent.CodexCLIHost, error) {
		return agent.CodexCLIHost{SocketPath: "/tmp/service.sock"}, nil
	}
	prepareCodexCLI = func(context.Context, agent.ACPAgentConfig, agent.CodexCLILaunchOptions) (agent.CodexCLILaunch, error) {
		return agent.CodexCLILaunch{Command: "/managed/codex", SocketPath: "/tmp/local.sock"}, nil
	}
	executed := false
	executeCodexCLI = func(context.Context, agent.CodexCLILaunch) error {
		executed = true
		return nil
	}

	err := runCodexCLI(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "不同 Codex Host") || executed {
		t.Fatalf("error=%v executed=%v, want socket mismatch rejection", err, executed)
	}
}

func TestRequestCodexCLIHostPreparationUsesRuntimeAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/codex/cli/prepare" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-WeClaw-Token") != "secret-token" {
			t.Fatalf("token=%q", r.Header.Get("X-WeClaw-Token"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","host":{"socket_path":"/tmp/shared.sock"}}`))
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.APIAddr = server.URL
	cfg.APIToken = "secret-token"

	host, err := requestCodexCLIHostPreparation(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if host.SocketPath != "/tmp/shared.sock" {
		t.Fatalf("host=%#v", host)
	}
}

func TestRequestCodexCLIHostPreparationDoesNotFollowRedirect(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.APIAddr = server.URL
	cfg.APIToken = "secret-token"

	_, err := requestCodexCLIHostPreparation(context.Background(), cfg)
	if err == nil || redirected {
		t.Fatalf("error=%v redirected=%v, want fail-closed local control request", err, redirected)
	}
}
