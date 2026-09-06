package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
	"github.com/gorilla/websocket"
)

func TestCodexAppSharedHostStartsAndSurvivesFrontendCancellation(t *testing.T) {
	if !codexAppSharedHostAvailable() {
		t.Skip("Codex App signed Node is unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "weclaw-app-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "app-server.sock")
	bridgeDir := codexAppBridgeDirectory(socket)
	if err := securefile.EnsureDir(bridgeDir); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexAppBridgeArtifact(filepath.Join(bridgeDir, "host.cjs"), codexAppHostScript, false); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: os.Args[0], Args: []string{"app-server"}, Cwd: dir,
		CodexHostMode: "shared",
		Env: map[string]string{
			testCodexUnixHostSocketEnv: socket,
			testCodexUnixHostCountEnv:  filepath.Join(dir, "starts.log"),
		},
	})
	// Keep identity validation real without scanning unrelated user processes.
	a.codexHostConflictPreflightCall = func(context.Context, int) error {
		_, err := a.validateManagedCodexHost(socket)
		return err
	}
	t.Cleanup(func() {
		metadata, err := a.readCodexHostMetadata(socket)
		if err != nil {
			return
		}
		process, err := os.FindProcess(metadata.PID)
		if err != nil {
			t.Error(err)
			return
		}
		defer process.Release()
		if err := process.Kill(); err != nil {
			t.Error(err)
			return
		}
		deadline := time.Now().Add(3 * time.Second)
		for codexHostProcessAlive(metadata.PID) {
			if time.Now().After(deadline) {
				t.Error("signed Node launcher did not reap its test Host")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := a.startCodexAppSharedHostLocked(ctx, socket, os.Args[0], []string{
		"-test.run=^TestHelperCodexUnixHost$", "-test.timeout=15s", "--",
	})
	if err != nil {
		t.Fatalf("start shared Host through signed Node: %v", err)
	}
	defer conn.Close()
	cancel()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]any{"id": 1, "method": "initialize", "params": codexInitializeParams()}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     int `json:"id"`
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatalf("shared Host stopped with its frontend: %v", err)
	}
	if response.ID != 1 || response.Result.ServerInfo.Name != "fake-codex-host" {
		t.Fatalf("unexpected shared Host handshake: %+v", response)
	}
}

func TestCodexAppBridgeMCPPreloadFollowsCurrentAppPipe(t *testing.T) {
	node := codexAppSharedNodePath()
	if info, err := os.Stat(node); err != nil || !info.Mode().IsRegular() {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Skip("Node is unavailable")
		}
	}
	dir := filepath.Join(t.TempDir(), "bridge with spaces")
	if err := securefile.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexAppBridgeArtifact(filepath.Join(dir, "mcp-env.mjs"), codexAppMCPEnvironmentScript, false); err != nil {
		t.Fatal(err)
	}
	args, _, err := normalizeCodexAppBridgeArgs([]string{"app-server", "-c", `mcp_servers.codex_app={env={CODEX_APP_TOOLS_PIPE_PATH="/tmp/first.sock"}}`}, dir)
	if err != nil {
		t.Fatal(err)
	}
	var options string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(args[len(args)-1], "mcp_servers.codex_app.env.NODE_OPTIONS=")), &options); err != nil {
		t.Fatal(err)
	}
	for _, pipe := range []string{"/tmp/first.sock", "/tmp/restarted app.sock"} {
		if _, err := saveCodexAppPipe(dir, pipe); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		command := exec.CommandContext(ctx, node, "-e", "process.stdout.write(process.env.CODEX_APP_TOOLS_PIPE_PATH)")
		command.Env = append(os.Environ(), "NODE_OPTIONS="+options, "CODEX_APP_TOOLS_PIPE_PATH=stale")
		output, err := command.CombinedOutput()
		cancel()
		if err != nil || string(output) != pipe {
			t.Fatalf("MCP pipe=%q want=%q err=%v", output, pipe, err)
		}
	}
}

func TestCodexAppBridgePreservesSettingsAcrossAppPipeChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "path with spaces")
	appConfig := `mcp_servers.codex_app={command="/app/official-mcp", args=["./server.mjs"], enabled=true, tools={send_message_to_thread={approval_mode="prompt"}}, env={CODEX_APP_TOOLS_PIPE_PATH="/tmp/first.sock", CODEX_MCP_NODE_PATH="/app/node"}}`
	args := []string{"-c", "features.code_mode_host=true", "app-server", "--stdio", "--analytics-default-enabled", "-c", appConfig}
	first, firstPipe, err := normalizeCodexAppBridgeArgs(args, dir)
	if err != nil {
		t.Fatal(err)
	}
	secondArgs := slices.Clone(args)
	secondArgs[len(secondArgs)-1] = strings.ReplaceAll(appConfig, "/tmp/first.sock", "/tmp/second.sock")
	second, secondPipe, err := normalizeCodexAppBridgeArgs(secondArgs, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, second) || firstPipe != "/tmp/first.sock" || secondPipe != "/tmp/second.sock" {
		t.Fatalf("App restart changed static launch configuration: %v %v", first, second)
	}
	for _, want := range []string{"features.code_mode_host=true", "--analytics-default-enabled", `approval_mode="prompt"`, `CODEX_MCP_NODE_PATH="/app/node"`} {
		if !strings.Contains(strings.Join(first, "\n"), want) {
			t.Fatalf("App configuration lost: %s", want)
		}
	}
	if args[3] != "--stdio" || !strings.Contains(args[len(args)-1], "/tmp/first.sock") {
		t.Fatal("mutated caller arguments")
	}
}

func TestCodexAppBridgeRejectsConflictingTransportAndPreload(t *testing.T) {
	for _, args := range [][]string{
		{"app-server", "--listen", "unix:///tmp/other.sock"},
		{"app-server", "--listen=stdio://"},
		{"app-server", "-c", `mcp_servers.codex_app={env={CODEX_APP_TOOLS_PIPE_PATH="/tmp/app.sock", NODE_OPTIONS="--inspect"}}`},
	} {
		if _, _, err := normalizeCodexAppBridgeArgs(args, t.TempDir()); err == nil {
			t.Fatalf("accepted conflicting configuration %v", args)
		}
	}
}

func TestExistingSharedHostConfiguresNextAppLaunch(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		CodexHostMode: "shared",
	})
	called := false
	a.codexAppSharedEnvironmentCall = func(_ context.Context, launcher string) error {
		called = true
		if launcher != "/tmp/weclaw-app-launcher" {
			t.Fatalf("launcher=%q", launcher)
		}
		return nil
	}
	if err := a.configureCodexAppSharedEnvironment(context.Background(), "/tmp/weclaw-app-launcher", false); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("existing shared Host did not configure the next App launch")
	}
}

func TestCodexAppBridgeRelaysAppJSONLWithoutChangingMessages(t *testing.T) {
	client, server := newCodexWebSocketPair(t)
	defer client.Close()
	defer server.Close()
	appInput, inputWriter := io.Pipe()
	defer appInput.Close()
	defer inputWriter.Close()
	appOutput, outputWriter := io.Pipe()
	defer appOutput.Close()
	defer outputWriter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- relayCodexAppBridge(ctx, client, appInput, outputWriter) }()
	request := `{"id":"app-1","method":"turn/steer","params":{"threadId":"original","expectedTurnId":"running","input":[{"type":"text","text":"继续原任务"}]}}`
	go func() { _, _ = io.WriteString(inputWriter, request+"\n") }()
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, message, err := server.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(message) != request {
		t.Fatalf("changed App request: %s", message)
	}
	response := `{"id":"app-1","result":{"turnId":"running"}}`
	if err := server.WriteMessage(websocket.TextMessage, []byte(response)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(appOutput).ReadString('\n')
	if err != nil || line != response+"\n" {
		t.Fatalf("App response=%q err=%v", line, err)
	}
	_ = inputWriter.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("App EOF did not release its connection relay")
	}
}

func TestCodexAppSharedHostDoesNotUsePrivateDesktopOwnership(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}, StateFile: filepath.Join(t.TempDir(), "state.json"), CodexHostMode: "shared", CodexDesktopBridge: true})
	if a.desktopProbe != nil || a.codexDesktopHostSelection || a.codexOwners.enforcesControl() {
		t.Fatal("shared App routing still depends on private Desktop ownership")
	}
	if !a.officialDaemonIsAuthoritativeForUnknownBinding() {
		t.Fatal("shared Host cannot read a thread with an unknown cached binding")
	}
	a.codexDesktopPresenceCall = func() (bool, bool) { return false, true }
	if !a.shouldDeferCodexAppDynamicToolCall() {
		t.Fatal("shared Host rejects a dynamic tool owned by the connected App")
	}
}
