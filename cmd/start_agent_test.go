package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/gorilla/websocket"
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
	t.Setenv("WECLAW_TEST_CODEX_RETRY_HELPER", "1")
	countFile := filepath.Join(t.TempDir(), "attempts")
	t.Setenv("WECLAW_TEST_CODEX_RETRY_COUNT", countFile)
	binDir := t.TempDir()
	codexPath := filepath.Join(binDir, "codex")
	if err := os.Symlink(os.Args[0], codexPath); err != nil {
		t.Fatalf("create codex helper symlink: %v", err)
	}
	oldDelay := codexACPStartupRetryDelay
	codexACPStartupRetryDelay = time.Millisecond
	t.Cleanup(func() { codexACPStartupRetryDelay = oldDelay })

	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{
		Type:    "acp",
		Command: codexPath,
		Args:    []string{"-test.run=TestHelperRetryingCodexAppServer", "app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
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
	if got := readRetryHelperAttempts(t, countFile); got != 3 {
		t.Fatalf("attempts=%d, want 3", got)
	}
}

func TestHelperRetryingCodexAppServer(t *testing.T) {
	if os.Getenv("WECLAW_TEST_CODEX_RETRY_HELPER") != "1" {
		return
	}
	countFile := os.Getenv("WECLAW_TEST_CODEX_RETRY_COUNT")
	attempt := readRetryHelperAttempts(t, countFile) + 1
	if err := os.WriteFile(countFile, []byte(fmt.Sprintf("%d", attempt)), 0o600); err != nil {
		t.Fatalf("write retry helper attempts: %v", err)
	}
	if attempt < 3 {
		fmt.Fprintln(os.Stderr, "Error: failed to initialize sqlite state runtime under /Users/dengtingru/.codex: failed to initialize state runtime at /Users/dengtingru/.codex")
		os.Exit(1)
	}
	socketPath := retryHelperCodexSocketPath(t, os.Args)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen retry helper socket: %v", err)
	}
	defer listener.Close()
	serveDone := make(chan error, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			conn, upgradeErr := (&websocket.Upgrader{}).Upgrade(writer, request, nil)
			if upgradeErr != nil {
				serveDone <- upgradeErr
				return
			}
			defer conn.Close()
			serveDone <- serveMinimalCodexInitialize(conn)
		}),
	}
	go func() { _ = server.Serve(listener) }()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve retry helper websocket: %v", err)
	}
	_ = server.Close()
	os.Exit(0)
}

func retryHelperCodexSocketPath(t *testing.T, args []string) string {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--listen" && strings.HasPrefix(args[index+1], "unix://") {
			return strings.TrimPrefix(args[index+1], "unix://")
		}
	}
	t.Fatalf("missing unix app-server listen argument: %q", args)
	return ""
}

func readRetryHelperAttempts(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read retry helper attempts: %v", err)
	}
	var attempts int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &attempts); err != nil {
		t.Fatalf("parse retry helper attempts: %v", err)
	}
	return attempts
}

func serveMinimalCodexInitialize(conn *websocket.Conn) error {
	_, line, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read initialize request: %w", err)
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return fmt.Errorf("parse initialize request: %w", err)
	}
	resp := json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos"}}`, req.ID))
	if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
		return fmt.Errorf("write initialize response: %w", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		return fmt.Errorf("read initialized notification: %w", err)
	}
	return nil
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
